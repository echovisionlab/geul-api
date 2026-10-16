package sitevariablemigrate

import (
	"context"

	"gorm.io/gorm"
)

func ensureBackupTable(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS site_variable_migration_backup (
  id bigserial PRIMARY KEY, run_id text NOT NULL, owner_key text NOT NULL, table_name text NOT NULL,
  row_key jsonb NOT NULL, before_image jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
 )`,
			// All rows from the old schema belong to the same legacy run. Remove
			// the default afterwards so new writers must supply their run ID.
			`ALTER TABLE site_variable_migration_backup ADD COLUMN IF NOT EXISTS run_id text NOT NULL DEFAULT 'legacy'`,
			`ALTER TABLE site_variable_migration_backup ALTER COLUMN run_id DROP DEFAULT`,
			`ALTER TABLE site_variable_migration_backup DROP CONSTRAINT IF EXISTS site_variable_migration_backup_table_name_row_key_key`,
			`CREATE UNIQUE INDEX IF NOT EXISTS site_variable_migration_backup_run_row_key ON site_variable_migration_backup(run_id, table_name, row_key)`,
		} {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
