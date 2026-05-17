package db

import (
	"errors"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jmoiron/sqlx"
)

func Migrate(db *sqlx.DB, migrations fs.FS) error {
	src, err := iofs.New(migrations, ".")
	if err != nil {
		return err
	}

	driver, err := sqlite.WithInstance(db.DB, &sqlite.Config{DatabaseName: "sqlite"})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithInstance("iofs", src, "sqlite", driver)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
