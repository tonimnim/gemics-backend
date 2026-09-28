// A one-shot deployment command; API replicas should use RUN_MIGRATIONS=false.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/database"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	writer := os.Getenv("DATABASE_WRITE_URL")
	db, err := database.Open(ctx, writer, writer, 2, 2)
	if err != nil {
		slog.Error("migration database configuration failed")
		os.Exit(1)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db.Writer); err != nil {
		slog.Error("migration failed", "error", err)
		os.Exit(1)
	}
	slog.Info("database migrations complete")
}
