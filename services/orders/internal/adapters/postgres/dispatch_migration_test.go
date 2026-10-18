package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func applyDispatchMigration(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	body, err := os.ReadFile("../../../migrations/000007_dispatch_snapshot.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(body)); err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile("../../../migrations/000008_pricing.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(body)); err != nil {
		t.Fatal(err)
	}
}
