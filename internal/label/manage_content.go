package label

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

type labelManageContentProjection struct {
	Document    *contentv1.RichTextDocument
	Revision    string
	SourceTitle string
}

// loadCreativeManageContentProjection reads the Label's source-locale
// document and title in one snapshot, after loadRoot reads the root row.
func loadCreativeManageContentProjection(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityID string,
	loadRoot func(context.Context, *gorm.DB) error,
) (labelManageContentProjection, error) {
	var projection labelManageContentProjection
	err := mutation.ReadLocked(ctx, db, func(tx *gorm.DB) error {
		if err := loadRoot(ctx, tx); err != nil {
			return err
		}
		snapshot, source, err := loadLabelContentSnapshot(ctx, tx, store, entityID)
		if err != nil {
			return err
		}
		document, err := contentblock.SnapshotToRichTextDocument(snapshot)
		if err != nil {
			return normalizeLabelContentBlockError(err)
		}
		var row struct {
			Title string `gorm:"column:title"`
		}
		if err := tx.WithContext(ctx).Table("label_translation").Select("title").
			Where("entity_id = ? AND locale = ?", entityID, source.SourceLocale).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domainerr.ResourceNotFound("label_translation", entityID)
			}
			return err
		}
		projection = labelManageContentProjection{
			Document: document, Revision: snapshot.Document.Revision.String(),
			SourceTitle: row.Title,
		}
		return nil
	})
	return projection, err
}

// initializeCreativeContentDocument replaces the new Label's empty content
// document with input, when the create request carries one.
func initializeCreativeContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityID string,
	sourceLocale string,
	created contentblock.Snapshot,
	input *contentv1.RichTextDocument,
) (contentblock.Result, error) {
	if input == nil {
		return contentblock.Result{}, nil
	}
	if input.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT {
		return contentblock.Result{}, domainerr.InvalidField("document.profile", "must be compact")
	}
	if strings.TrimSpace(input.GetSourceLocale()) != sourceLocale {
		return contentblock.Result{}, domainerr.InvalidField("document.source_locale", "must match the server-selected source locale")
	}
	replace, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, input)
	if err != nil {
		return contentblock.Result{}, normalizeLabelContentBlockError(err)
	}
	result, err := store.ReplaceSnapshot(ctx, tx, replace, labelContentCreationFence(entityID, sourceLocale))
	if err != nil {
		return contentblock.Result{}, normalizeLabelContentBlockError(err)
	}
	return result, nil
}
