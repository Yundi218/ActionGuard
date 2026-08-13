package commerce

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCaptureRollbackErrorJoinsPrimaryFailure(t *testing.T) {
	primary := errors.New("primary failure")
	rollback := errors.New("rollback failure")
	err := primary

	captureRollbackError(context.Background(), func(context.Context) error { return rollback }, &err)

	if !errors.Is(err, primary) || !errors.Is(err, rollback) {
		t.Fatalf("joined error = %v", err)
	}
}

func TestCaptureRollbackErrorReportsStandaloneFailure(t *testing.T) {
	rollback := errors.New("rollback failure")
	var err error

	captureRollbackError(context.Background(), func(context.Context) error { return rollback }, &err)

	if !errors.Is(err, rollback) {
		t.Fatalf("error = %v, want rollback failure", err)
	}
}

func TestCaptureRollbackErrorIgnoresClosedTransaction(t *testing.T) {
	var err error

	captureRollbackError(context.Background(), func(context.Context) error { return pgx.ErrTxClosed }, &err)

	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
}
