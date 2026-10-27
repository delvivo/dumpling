package store

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order on top of schema.sql; PRAGMA user_version counts applied entries.
// Only additive changes (ADD COLUMN): SQLite CHECK constraints cannot change without a table rebuild.
var migrations = [][]string{
	{ // 1: remote destinations (002-s3-sftp-destinations)
		`ALTER TABLE destinations ADD COLUMN type TEXT NOT NULL DEFAULT 'local'`,
		`ALTER TABLE destinations ADD COLUMN config TEXT NOT NULL DEFAULT '{}'`,
		`ALTER TABLE destinations ADD COLUMN secret TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE backups ADD COLUMN imported INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE backups ADD COLUMN origin_plan_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE backups ADD COLUMN passphrase TEXT NOT NULL DEFAULT ''`,
	},
}

func migrate(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		for _, q := range migrations[i] {
			if _, err := tx.Exec(q); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
