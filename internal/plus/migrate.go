package plus

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

// listmonk-plus keeps its own migration track, separate from upstream's
// version-keyed migrations in cmd/upgrade.go, so that our schema changes never
// collide with upstream's future versions. See plus/docs/001-sequences-design.md §2.

//go:embed sql/migrations/*.sql
var migrationsFS embed.FS

//go:embed sql/triggers.sql
var triggersSQL string

// advisoryLockKey serialises migrations if several instances start at once.
// Arbitrary constant ("plus" in ASCII).
const advisoryLockKey = 0x706c7573

// Migrate applies pending listmonk-plus migrations (each in its own transaction,
// recorded in plus_migrations) and then (re)applies the idempotent triggers on
// upstream tables. It is safe to call on every startup.
func Migrate(db *sqlx.DB, lo *log.Logger) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS plus_migrations (
		id         TEXT PRIMARY KEY,
		applied_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("creating plus_migrations: %w", err)
	}

	files, err := fs.Glob(migrationsFS, "sql/migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, f := range files {
		id := strings.TrimSuffix(f[strings.LastIndex(f, "/")+1:], ".sql")
		applied, err := applyMigration(db, id, f)
		if err != nil {
			return fmt.Errorf("plus migration %s: %w", id, err)
		}
		if applied {
			lo.Printf("listmonk-plus: applied migration %s", id)
		}
	}

	// Triggers are re-applied every time (see the header of sql/triggers.sql).
	tx, err := db.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		return err
	}
	if _, err := tx.Exec(triggersSQL); err != nil {
		return fmt.Errorf("applying plus triggers: %w", err)
	}
	return tx.Commit()
}

// applyMigration runs one migration file if it hasn't been applied yet.
func applyMigration(db *sqlx.DB, id, file string) (bool, error) {
	body, err := migrationsFS.ReadFile(file)
	if err != nil {
		return false, err
	}

	tx, err := db.Beginx()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock($1)`, advisoryLockKey); err != nil {
		return false, err
	}

	var done bool
	if err := tx.Get(&done, `SELECT EXISTS(SELECT 1 FROM plus_migrations WHERE id = $1)`, id); err != nil {
		return false, err
	}
	if done {
		return false, nil
	}

	if _, err := tx.Exec(string(body)); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`INSERT INTO plus_migrations (id) VALUES ($1)`, id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
