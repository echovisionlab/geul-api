package legal

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func snapshotHasSiteVariables(snapshot contentblock.Snapshot) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		for _, block := range overlay.Blocks {
			if strings.Contains(string(block.LocalizedData), `"siteVariable"`) || strings.Contains(string(block.LocalizedData), "{{site.") || strings.Contains(string(block.LocalizedData), "{{contact.") {
				return true
			}
		}
	}
	return false
}

type migrationFileReuseDenied struct{}

func (migrationFileReuseDenied) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return contentblock.ErrFileReference
}

// RegenerateSiteVariableMigrationProjections uses the Policy owner's canonical
// renderer inside the migration's existing transaction. It never activates a
// version or schedules notices. Historical snapshots are not rewritten.
func RegenerateSiteVariableMigrationProjections(ctx context.Context, tx *gorm.DB, documentID, runID string) error {
	store, err := contentblock.NewGeneratedStore(migrationFileReuseDenied{})
	if err != nil {
		return err
	}
	for _, kind := range []string{"privacy", "terms"} {
		var root struct{ ID, SourceLocale string }
		result := tx.Table(kind+"_history").Select("id, source_locale").Where("content_document_id = ?", documentID).Scan(&root)
		if result.Error != nil {
			return result.Error
		}
		if root.ID == "" {
			continue
		}
		// Back up derived projections before the owning renderer updates them.
		if err := tx.Exec(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) SELECT ?, ?, ?, jsonb_build_object('id',t.id), to_jsonb(t) FROM `+kind+`_history t WHERE id = ? ON CONFLICT(run_id,table_name,row_key) DO NOTHING`, runID, "document:"+documentID, kind+"_history", root.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) SELECT ?, ?, ?, jsonb_build_object('entity_id',t.entity_id,'locale',t.locale), to_jsonb(t) FROM `+kind+`_translation t WHERE entity_id = ? ON CONFLICT(run_id,table_name,row_key) DO NOTHING`, runID, "document:"+documentID, kind+"_translation", root.ID).Error; err != nil {
			return err
		}
		snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, uuid.MustParse(documentID), root.SourceLocale)
		if err != nil {
			return err
		}
		if err := (internalLegalDocumentService{kind: kind}).refreshDerivedContentProjectionsWithDB(ctx, tx, root.ID, snapshot, root.SourceLocale, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}
