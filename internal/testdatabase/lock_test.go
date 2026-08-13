package testdatabase

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Yundi218/ActionGuard/internal/database"
)

func TestAcquireAdvisoryLockHonorsContextTimeout(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := database.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const key int64 = publicSchemaLockKey + 1
	first, err := acquireAdvisoryLock(context.Background(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Release(context.Background()); err != nil {
			t.Errorf("release first lock: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	second, err := acquireAdvisoryLock(ctx, pool, key)
	if second != nil {
		t.Fatal("second lock unexpectedly acquired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("lock timeout took %s", elapsed)
	}
}
