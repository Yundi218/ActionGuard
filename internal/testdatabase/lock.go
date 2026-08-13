package testdatabase

import (
	"context"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

const publicSchemaLockKey int64 = 0x4163744775617264

type AdvisoryLock struct {
	conn     *pgxpool.Conn
	key      int64
	mu       sync.Mutex
	released bool
}

func AcquirePublicSchemaLock(ctx context.Context, pool *pgxpool.Pool) (*AdvisoryLock, error) {
	return acquireAdvisoryLock(ctx, pool, publicSchemaLockKey)
}

func acquireAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key int64) (*AdvisoryLock, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, `select pg_advisory_lock($1)`, key); err != nil {
		conn.Release()
		return nil, err
	}
	return &AdvisoryLock{conn: conn, key: key}, nil
}

func (lock *AdvisoryLock) Release(ctx context.Context) error {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released {
		return nil
	}
	lock.released = true
	defer lock.conn.Release()

	var unlocked bool
	if err := lock.conn.QueryRow(ctx, `select pg_advisory_unlock($1)`, lock.key).Scan(&unlocked); err != nil {
		return err
	}
	if !unlocked {
		return fmt.Errorf("PostgreSQL advisory lock %d was not held", lock.key)
	}
	return nil
}
