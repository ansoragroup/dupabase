package database

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestMigrationBatchAtomicityAndConcurrentStartup(t *testing.T) {
	dsn := os.Getenv("DUPABASE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("DUPABASE_TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || !strings.HasPrefix(u.Path, "/dupabase_maintenance_") {
		t.Fatal("requires an explicitly disposable loopback database named dupabase_maintenance_*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := NewPlatformPool(ctx, dsn, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	table := "maintenance_migration_" + suffix
	name := "maintenance-migration-" + suffix
	identifier := pgx.Identifier{"platform", table}.Sanitize()
	defer func() {
		_, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+identifier)
		_, _ = pool.Exec(ctx, `DELETE FROM platform._migrations WHERE name=$1`, name)
	}()
	batch := []Migration{{Name: name, SQL: "CREATE TABLE " + identifier + " (value int); INSERT INTO " + identifier + " VALUES (1); SELECT pg_sleep(0.02);"}}
	// The failed second migration must also roll back the successful first.
	failed := append(append([]Migration{}, batch...), Migration{Name: name + "-fail", SQL: "SELECT nonexistent_migration_function();"})
	if err := RunMigrations(ctx, pool, failed); err == nil {
		t.Fatal("invalid batch succeeded")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "platform."+table).Scan(&exists); err != nil || exists {
		t.Fatalf("failed batch left a committed table: %v", err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errors <- RunMigrations(ctx, pool, batch) })
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal("concurrent startup failed", err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+identifier).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration executed more than once: count=%d error=%v", count, err)
	}
	if err := RunMigrations(ctx, pool, batch); err != nil {
		t.Fatal("restart failed", err)
	}
}
