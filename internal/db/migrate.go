package db

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

func Migrate(db *sqlx.DB, migrations fs.FS) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
		)
	`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	entries, err := fs.Glob(migrations, "*.up.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(entries)

	for _, name := range entries {
		var count int
		if err := db.Get(&count, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, name); err != nil {
			return fmt.Errorf("checking migration %s: %w", name, err)
		}
		if count > 0 {
			continue
		}

		data, err := fs.ReadFile(migrations, name)
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", name, err)
		}

		for _, stmt := range splitStatements(string(data)) {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("executing migration %s: %w", name, err)
			}
		}

		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			return fmt.Errorf("recording migration %s: %w", name, err)
		}
	}

	return nil
}

func splitStatements(sql string) []string {
	var stmts []string
	for _, s := range strings.Split(sql, ";") {
		s = strings.TrimSpace(s)
		if s != "" {
			stmts = append(stmts, s)
		}
	}
	return stmts
}
