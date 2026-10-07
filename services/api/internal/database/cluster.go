package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Cluster struct {
	Writer *pgxpool.Pool
	Reader *pgxpool.Pool
}

var trustedLegacyMigrationChecksums = map[string]string{
	"000001_core.up.sql":     "46456006cdffdb047340c9d292895b4b95f4e822fec07e43c1f2cfec1badc6b1",
	"000002_identity.up.sql": "16098812e19fb205b6837dc1b8192f9698aba34002f19e5c9ac6267fc42e8b5b",
}

func Open(ctx context.Context, writeURL, readURL string, writeMax, readMax int32) (*Cluster, error) {
	if writeURL == "" {
		return nil, fmt.Errorf("database writer URL is required")
	}
	if readURL == "" {
		readURL = writeURL
	}
	writer, err := openPool(ctx, writeURL, writeMax)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	if readURL == writeURL {
		return &Cluster{Writer: writer, Reader: writer}, nil
	}
	reader, err := openPool(ctx, readURL, readMax)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	return &Cluster{Writer: writer, Reader: reader}, nil
}

func openPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MinConns = 0
	cfg.MinIdleConns = 0
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.PingTimeout = 5 * time.Second
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	return pgxpool.NewWithConfig(ctx, cfg)
}

func (c *Cluster) Close() {
	if c == nil {
		return
	}
	if c.Reader != nil && c.Reader != c.Writer {
		c.Reader.Close()
	}
	if c.Writer != nil {
		c.Writer.Close()
	}
}

func (c *Cluster) PingWriter(ctx context.Context) error { return c.Writer.Ping(ctx) }
func (c *Cluster) PingReader(ctx context.Context) error { return c.Reader.Ping(ctx) }

// ReaderLag reports how far the replica trails the writer.
//
// The timestamp of the last replayed transaction is only meaningful while writes
// are flowing. On an idle cluster pg_last_xact_replay_timestamp stays fixed while
// wall-clock advances, so a perfectly caught-up replica would report ever-growing
// lag and every read would fall back to the writer during exactly the quiet
// periods when the replica is most trustworthy.
//
// Comparing the received and replayed LSNs answers the real question: has this
// replica applied everything it has been sent? If so the lag is zero whatever the
// clock says, and the timestamp is consulted only when replay is genuinely behind.
func (c *Cluster) ReaderLag(ctx context.Context) (time.Duration, error) {
	var seconds float64
	err := c.Reader.QueryRow(ctx, `SELECT CASE
		WHEN NOT pg_is_in_recovery() THEN 0
		WHEN pg_last_wal_receive_lsn() IS NOT NULL
			AND pg_last_wal_receive_lsn()=pg_last_wal_replay_lsn() THEN 0
		ELSE COALESCE(EXTRACT(EPOCH FROM now()-pg_last_xact_replay_timestamp()),0)
		END`).Scan(&seconds)
	return time.Duration(seconds * float64(time.Second)), err
}

func Migrate(ctx context.Context, writer *pgxpool.Pool) error {
	const lockID int64 = 7146249729104039
	conn, err := writer.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		conn.Release()
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRow(unlockCtx, "SELECT pg_advisory_unlock($1)", lockID).Scan(&unlocked)
		if unlockErr != nil || !unlocked {
			raw := conn.Hijack()
			_ = raw.Close(context.Background())
			return
		}
		conn.Release()
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version text PRIMARY KEY,
		checksum text,
        applied_at timestamptz NOT NULL DEFAULT now()
	);
	ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum text`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var applied bool
		raw, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(raw))
		var storedChecksum *string
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1),
			(SELECT checksum FROM schema_migrations WHERE version=$1)`, name).Scan(&applied, &storedChecksum); err != nil {
			return err
		}
		if applied {
			if storedChecksum != nil && *storedChecksum != checksum {
				return fmt.Errorf("migration %s checksum changed after application", name)
			}
			if storedChecksum == nil {
				trusted, ok := trustedLegacyMigrationChecksums[name]
				if !ok || trusted != checksum {
					return fmt.Errorf("migration %s has no trusted legacy checksum", name)
				}
				if _, err := conn.Exec(ctx, "UPDATE schema_migrations SET checksum=$2 WHERE version=$1", name, checksum); err != nil {
					return fmt.Errorf("record checksum for %s: %w", name, err)
				}
			}
			continue
		}
		sql := strings.TrimSpace(string(raw))
		sql = strings.TrimSpace(strings.TrimPrefix(sql, "BEGIN;"))
		sql = strings.TrimSpace(strings.TrimSuffix(sql, "COMMIT;"))
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, sql); err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES ($1,$2)", name, checksum)
		}
		if err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}
