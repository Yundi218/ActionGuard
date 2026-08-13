package database

import (
	"context"
	"io/fs"

	"github.com/Yundi218/ActionGuard/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	files, err := migrationFileNames(migrations.FS)
	if err != nil {
		return err
	}
	for _, name := range files {
		migration, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(migration)); err != nil {
			return err
		}
	}
	return nil
}

func migrationFileNames(filesystem fs.FS) ([]string, error) {
	return fs.Glob(filesystem, "[0-9][0-9][0-9]_*.sql")
}
