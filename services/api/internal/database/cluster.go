package database

import (
	"context"
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
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
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

func Migrate(ctx context.Context, writer *pgxpool.Pool) error {
	const lockID int64 = 7146249729104039
	if _, err := writer.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer writer.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockID) //nolint:errcheck

	if _, err := writer.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version text PRIMARY KEY,
        applied_at timestamptz NOT NULL DEFAULT now()
    )`); err != nil {
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
		if err := writer.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)", name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		raw, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		sql := strings.TrimSpace(string(raw))
		sql = strings.TrimSpace(strings.TrimPrefix(sql, "BEGIN;"))
		sql = strings.TrimSpace(strings.TrimSuffix(sql, "COMMIT;"))
		tx, err := writer.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, sql); err == nil {
			_, err = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES ($1)", name)
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
