package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Desu-php/food/services/catalog/internal/application"
	"github.com/Desu-php/food/services/catalog/internal/domain"
)

func integrationPool(t *testing.T, migrations ...string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated PostgreSQL test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("catalog_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if len(migrations) == 0 {
		migrations = []string{"000001_restaurants", "000003_restaurant_address"}
	}
	if len(migrations) > 1 && migrations[len(migrations)-1] != "000003_restaurant_address" {
		migrations = append(migrations, "000003_restaurant_address")
	}
	if len(migrations) > 1 {
		migrations = append(migrations, "000004_restaurant_geolocation", "000005_pricing")
	}
	for _, migration := range migrations {
		sql, err := os.ReadFile("../../../migrations/" + migration + ".up.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("apply %s: %v", migration, err)
		}
	}
	return pool
}

func TestPostgresRestaurantRepository(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRestaurants(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := repo.List(ctx, application.Page{Limit: 20})
	if err != nil || len(rows) != 2 || rows[0].ID != 1 || rows[0].Name != "Pizza House" {
		t.Fatalf("seed: %+v %v", rows, err)
	}
	created, err := repo.Create(ctx, domain.Restaurant{Name: "Cafe", IsOpen: true})
	if err != nil || created.ID != 3 || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("create: %+v %v", created, err)
	}
	got, err := repo.Get(ctx, created.ID)
	if err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("get: %+v %v", got, err)
	}
	closed := false
	updated, err := repo.Update(ctx, created.ID, domain.RestaurantChanges{IsOpen: &closed})
	if err != nil || updated.IsOpen || updated.Name != created.Name || !updated.CreatedAt.Equal(created.CreatedAt) || !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("update: %+v %v", updated, err)
	}
	name := "Renamed"
	updated, err = repo.Update(ctx, created.ID, domain.RestaurantChanges{Name: &name})
	if err != nil || updated.Name != name || updated.IsOpen {
		t.Fatalf("rename: %+v %v", updated, err)
	}
	got, err = repo.Get(ctx, created.ID)
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("persisted update: %+v %v", got, err)
	}
	rows, err = repo.List(ctx, application.Page{Limit: 1, Offset: 2})
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], updated) {
		t.Fatalf("page: %+v %v", rows, err)
	}
	rows, err = repo.List(ctx, application.Page{Limit: 20, Offset: 999})
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty page: %+v %v", rows, err)
	}
	if _, err := repo.Get(ctx, 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing get: %v", err)
	}
	if _, err := repo.Update(ctx, 999, domain.RestaurantChanges{Name: &name}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing update: %v", err)
	}
	injection := `Robert'); DROP TABLE restaurants; --`
	injected, err := repo.Create(ctx, domain.Restaurant{Name: injection})
	if err != nil || injected.Name != injection {
		t.Fatalf("parameterized create: %+v %v", injected, err)
	}
	if _, err := repo.Get(ctx, created.ID); err != nil {
		t.Fatalf("table lost: %v", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := repo.Get(canceled, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
	if _, err := repo.Create(ctx, domain.Restaurant{Name: ""}); err == nil {
		t.Fatal("database accepted empty name")
	}
}

func TestPostgresConcurrentWrites(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRestaurants(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	original, err := repo.Create(ctx, domain.Restaurant{Name: "Original", IsOpen: true})
	if err != nil {
		t.Fatal(err)
	}
	name := "Renamed"
	closed := false
	var wg sync.WaitGroup
	for _, changes := range []domain.RestaurantChanges{{Name: &name}, {IsOpen: &closed}} {
		wg.Add(1)
		go func(c domain.RestaurantChanges) {
			defer wg.Done()
			if _, err := repo.Update(ctx, original.ID, c); err != nil {
				t.Error(err)
			}
		}(changes)
	}
	wg.Wait()
	got, err := repo.Get(ctx, original.ID)
	if err != nil || got.Name != name || got.IsOpen {
		t.Fatalf("concurrent fields overwritten: %+v %v", got, err)
	}
	const count = 12
	ids := make(chan int64, count)
	for range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := repo.Create(ctx, domain.Restaurant{Name: "Concurrent"})
			if err != nil {
				t.Error(err)
				return
			}
			ids <- r.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[int64]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("unique ids: %d, want %d", len(seen), count)
	}
	rows, err := repo.List(ctx, application.Page{Limit: 100})
	if err != nil || len(rows) != count+3 {
		t.Fatalf("lost inserts: count=%d error=%v", len(rows), err)
	}
}

func TestMigrationRollback(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pricingDown, err := os.ReadFile("../../../migrations/000005_pricing.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(pricingDown)); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("../../../migrations/000001_restaurants.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('restaurants') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("table still exists: %v %v", exists, err)
	}
}

func TestRestaurantAddressPersistence(t *testing.T) {
	pool := integrationPool(t)
	repo := NewRestaurants(pool)
	ctx := context.Background()
	r, err := repo.Create(ctx, domain.Restaurant{Name: "Pickup restaurant", Address: "Original location"})
	if err != nil {
		t.Fatal(err)
	}
	address := "New restaurant location"
	if _, err := repo.Update(ctx, r.ID, domain.RestaurantChanges{Address: &address}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, r.ID)
	if err != nil || got.Address != address {
		t.Fatal(got, err)
	}
	invalid := " "
	if _, err := repo.Update(ctx, r.ID, domain.RestaurantChanges{Address: &invalid}); err == nil {
		t.Fatal("invalid address persisted")
	}
}
