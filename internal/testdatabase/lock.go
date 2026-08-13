package testdatabase

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const publicSchemaLockKey int64 = 0x4163744775617264

const advisoryLockCloseTimeout = 5 * time.Second

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
		return nil, joinWithSessionClose(err, conn)
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

	var unlocked bool
	if err := lock.conn.QueryRow(ctx, `select pg_advisory_unlock($1)`, lock.key).Scan(&unlocked); err != nil {
		return joinWithSessionClose(err, lock.conn)
	}
	if !unlocked {
		return joinWithSessionClose(fmt.Errorf("PostgreSQL advisory lock %d was not held", lock.key), lock.conn)
	}
	lock.conn.Release()
	return nil
}

func joinWithSessionClose(err error, conn *pgxpool.Conn) error {
	physicalConn := conn.Hijack()
	closeCtx, cancel := context.WithTimeout(context.Background(), advisoryLockCloseTimeout)
	defer cancel()
	if closeErr := physicalConn.Close(closeCtx); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	return err
}
