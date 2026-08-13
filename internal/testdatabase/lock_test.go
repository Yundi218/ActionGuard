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

func TestReleaseWithCanceledContextClosesSessionAndFreesLock(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	holderPool, err := database.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(holderPool.Close)
	waiterPool, err := database.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(waiterPool.Close)

	const key int64 = publicSchemaLockKey + 2
	lock, err := acquireAdvisoryLock(context.Background(), holderPool, key)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lock.Release(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("release error = %v, want context canceled", err)
	}

	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), time.Second)
	defer cancelAcquire()
	reacquired, err := acquireAdvisoryLock(acquireCtx, waiterPool, key)
	if err != nil {
		t.Fatalf("independent pool did not promptly acquire released lock: %v", err)
	}
	if err := reacquired.Release(context.Background()); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}

func TestReleaseUnlocksAndIsIdempotent(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	pool, err := database.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const key int64 = publicSchemaLockKey + 3
	first, err := acquireAdvisoryLock(context.Background(), pool, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatalf("second release: %v", err)
	}

	second, err := acquireAdvisoryLock(context.Background(), pool, key)
	if err != nil {
		t.Fatalf("reacquire after normal release: %v", err)
	}
	if err := second.Release(context.Background()); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
}
