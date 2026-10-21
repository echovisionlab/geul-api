package displaysizemigrate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var versionTables = []struct{ table, kind string }{
	{"page_version", "page"},
	{"post_version", "post"},
	{"work_version", "work"},
}

type blockChange struct {
	id, before, after string
}

type snapshotChange struct {
	table, id, before, after string
}

type plan struct {
	documents map[string][]blockChange
	snapshots []snapshotChange
}

// Run reports, and with apply writes, the rename for every live Block and
// version snapshot. Callers must stop content writers and close collaboration
// rooms before apply; this maintenance path emits no content invalidation.
func Run(ctx context.Context, db *gorm.DB, apply bool, report io.Writer) error {
	return run(ctx, db, apply, false, report)
}

// Verify performs the same inventory without writing and fails while any live
// Block or version snapshot still needs conversion.
func Verify(ctx context.Context, db *gorm.DB, report io.Writer) error {
	return run(ctx, db, false, true, report)
}

func run(ctx context.Context, db *gorm.DB, apply, requireClean bool, report io.Writer) error {
	if err := requireTargetCatalog(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(report, "target_catalog=%s\n", targetCatalog); err != nil {
		return err
	}
	planned, err := inventory(ctx, db)
	if err != nil {
		return err
	}
	blocks := 0
	for _, changes := range planned.documents {
		blocks += len(changes)
	}
	if _, err := fmt.Fprintf(report, "content_block changes=%d documents=%d\nversion_snapshot changes=%d apply=%t\n", blocks, len(planned.documents), len(planned.snapshots), apply); err != nil {
		return err
	}
	pending := blocks + len(planned.snapshots)
	if requireClean && pending != 0 {
		return fmt.Errorf("display size verification failed: %d rows still require conversion", pending)
	}
	if !apply || pending == 0 {
		return nil
	}
	if err := ensureBackupTable(ctx, db); err != nil {
		return err
	}
	runID := uuid.NewString()
	if _, err := fmt.Fprintf(report, "run_id=%s\n", runID); err != nil {
		return err
	}
	documentIDs := make([]string, 0, len(planned.documents))
	for id := range planned.documents {
		documentIDs = append(documentIDs, id)
	}
	sort.Strings(documentIDs)
	for _, documentID := range documentIDs {
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return applyDocument(tx, runID, documentID, planned.documents[documentID])
		}); err != nil {
			return fmt.Errorf("document %s: %w", documentID, err)
		}
	}
	for _, change := range planned.snapshots {
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return applyRow(tx, runID, change.table, "content_snapshot", change.id, change.before, change.after)
		}); err != nil {
			return fmt.Errorf("%s %s: %w", change.table, change.id, err)
		}
	}
	return nil
}

func inventory(ctx context.Context, db *gorm.DB) (plan, error) {
	planned := plan{documents: map[string][]blockChange{}}
	var localized int64
	if err := db.WithContext(ctx).Raw(`SELECT count(*) FROM content_block_locale WHERE localized_data::text LIKE ?`, "%\""+legacyField+"\"%").Scan(&localized).Error; err != nil {
		return plan{}, err
	}
	if localized != 0 {
		return plan{}, fmt.Errorf("%d localized Block rows hold the shared %s field", localized, legacyField)
	}
	var blocks []struct {
		ID, DocumentID, Kind, Profile string
		SharedData                    string
	}
	if err := db.WithContext(ctx).Raw(`SELECT b.id, b.document_id, b.kind, d.profile, b.shared_data::text AS shared_data FROM content_block b JOIN content_document d ON d.id = b.document_id WHERE b.shared_data::text LIKE ? ORDER BY b.document_id, b.id`, "%\""+legacyField+"\"%").Scan(&blocks).Error; err != nil {
		return plan{}, err
	}
	for _, block := range blocks {
		after, changed, err := LiveSharedJSON(block.Profile, block.Kind, []byte(block.SharedData))
		if err != nil {
			return plan{}, fmt.Errorf("Block %s: %w", block.ID, err)
		}
		if changed {
			planned.documents[block.DocumentID] = append(planned.documents[block.DocumentID], blockChange{block.ID, block.SharedData, string(after)})
		}
	}
	for _, version := range versionTables {
		var rows []struct{ ID, ContentSnapshot string }
		// Identifiers come only from the static table list above.
		if err := db.WithContext(ctx).Raw(`SELECT id::text AS id, content_snapshot::text AS content_snapshot FROM "`+version.table+`" WHERE content_snapshot->'document'->>'blockCatalogFingerprint' IS DISTINCT FROM ? OR content_snapshot::text LIKE ? ORDER BY id`, targetCatalog, "%\""+legacyField+"\"%").Scan(&rows).Error; err != nil {
			return plan{}, err
		}
		for _, row := range rows {
			after, changed, err := SnapshotJSON([]byte(row.ContentSnapshot), version.kind)
			if err != nil {
				return plan{}, fmt.Errorf("%s %s: %w", version.table, row.ID, err)
			}
			if changed {
				planned.snapshots = append(planned.snapshots, snapshotChange{version.table, row.ID, row.ContentSnapshot, string(after)})
			}
		}
	}
	return planned, nil
}

func applyDocument(tx *gorm.DB, runID, documentID string, changes []blockChange) error {
	if err := tx.Exec(`SELECT id FROM content_document WHERE id = ? FOR UPDATE`, documentID).Error; err != nil {
		return err
	}
	for _, change := range changes {
		if err := applyRow(tx, runID, "content_block", "shared_data", change.id, change.before, change.after); err != nil {
			return err
		}
	}
	if err := backupRow(tx, runID, "content_document", documentID); err != nil {
		return err
	}
	return tx.Table("content_document").Where("id = ?", documentID).UpdateColumn("revision", uuid.New()).Error
}

// applyRow backs up the locked row and rewrites one JSON column. The column
// predicate rejects a row that changed after the plan was made.
func applyRow(tx *gorm.DB, runID, table, column, id, before, after string) error {
	if err := backupRow(tx, runID, table, id); err != nil {
		return err
	}
	// Identifiers come only from the static migration tables.
	result := tx.Exec(`UPDATE "`+table+`" SET "`+column+`" = ?::jsonb WHERE id = ? AND "`+column+`" = ?::jsonb`, after, id, before)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("migration conflict: %s %s changed after planning", table, id)
	}
	return nil
}

func backupRow(tx *gorm.DB, runID, table, id string) error {
	key, _ := json.Marshal(map[string]string{"id": id})
	var locked struct{ Row string }
	if err := tx.Raw(`SELECT to_jsonb(t)::text AS row FROM "`+table+`" t WHERE id = ? FOR UPDATE`, id).Scan(&locked).Error; err != nil {
		return err
	}
	if locked.Row == "" {
		return fmt.Errorf("migration row disappeared: %s %s", table, id)
	}
	return tx.Exec(`INSERT INTO display_size_migration_backup(run_id, table_name, row_key, before_image) VALUES (?, ?, ?::jsonb, ?::jsonb) ON CONFLICT (run_id, table_name, row_key) DO NOTHING`, runID, table, string(key), locked.Row).Error
}

func ensureBackupTable(ctx context.Context, db *gorm.DB) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS display_size_migration_backup (
  id bigserial PRIMARY KEY, run_id text NOT NULL, table_name text NOT NULL,
  row_key jsonb NOT NULL, before_image jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
 )`,
			`CREATE UNIQUE INDEX IF NOT EXISTS display_size_migration_backup_run_row_key ON display_size_migration_backup(run_id, table_name, row_key)`,
		} {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
