package database

import (
	"context"

	"github.com/Yundi218/ActionGuard/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	commerceMigration, err := migrations.FS.ReadFile("001_commerce.sql")
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, string(commerceMigration))
	return err
}
