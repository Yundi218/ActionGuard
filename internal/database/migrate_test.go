package database

import (
	"context"
	"os"
	"testing"
)

func TestMigrateCreatesOrders(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	pool, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}

	var table string
	err = pool.QueryRow(context.Background(), `select to_regclass('public.orders')::text`).Scan(&table)
	if err != nil || table != "orders" {
		t.Fatalf("table = %q, err = %v", table, err)
	}
}
