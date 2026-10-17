package httpapi

import (
	"crypto/rand"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// readYourWritesServer has distinct writer and reader pools that are never
// queried: selfReader only chooses between them.
func readYourWritesServer(client *redis.Client) (*Server, *pgxpool.Pool, *pgxpool.Pool) {
	writer, reader := &pgxpool.Pool{}, &pgxpool.Pool{}
	return &Server{
		db:     &database.Cluster{Writer: writer, Reader: reader},
		redis:  client,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: config.Config{DatabaseMaxReplicaLag: 400 * time.Millisecond, CacheNamespace: "test:" + rand.Text()},
	}, writer, reader
}

func TestSelfReaderFailsSafeToTheWriter(t *testing.T) {
	// Without Redis nobody can tell whether the replica has the player's last
	// write, so their own reads go to the writer.
	server, writer, _ := readYourWritesServer(nil)
	if server.selfReader(t.Context(), "player") != writer {
		t.Error("selfReader without Redis did not choose the writer")
	}
	// A single pool is both, and nothing is marked.
	single := &pgxpool.Pool{}
	server.db = &database.Cluster{Writer: single, Reader: single}
	server.markRecentWrite(t.Context(), "player")
	if server.selfReader(t.Context(), "player") != single {
		t.Error("selfReader with one pool did not return it")
	}
	// Redis that can't be reached fails safe too.
	unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = unreachable.Close() })
	server, writer, _ = readYourWritesServer(unreachable)
	server.markRecentWrite(t.Context(), "player")
	if server.selfReader(t.Context(), "player") != writer {
		t.Error("selfReader with unreachable Redis did not choose the writer")
	}
}

func TestIntegrationSelfReaderFollowsRecentWrites(t *testing.T) {
	server, writer, reader := readYourWritesServer(evidenceLimitsRedis(t))
	ctx := t.Context()
	if server.selfReader(ctx, "kamau") != reader {
		t.Fatal("a player with no recent write did not read from the replica")
	}
	server.markRecentWrite(ctx, "kamau")
	if server.selfReader(ctx, "kamau") != writer {
		t.Fatal("a player who just wrote did not read from the writer")
	}
	if server.selfReader(ctx, "wanjiru") != reader {
		t.Fatal("another player's write moved this player's reads to the writer")
	}
	// The mark lasts DatabaseMaxReplicaLag, after which the replica has the write.
	time.Sleep(600 * time.Millisecond)
	if server.selfReader(ctx, "kamau") != reader {
		t.Fatal("reads did not return to the replica after the window")
	}
}
