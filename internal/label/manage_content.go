package label

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"gorm.io/gorm"
)

type labelManageContentProjection struct {
	Document    *contentv1.RichTextDocument
	Revision    string
	SourceTitle string
}

func loadCreativeManageContentProjection(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	loadRoot ...func(context.Context, *gorm.DB) error,
) (labelManageContentProjection, error) {
	if entityType != labelContentEntity {
		return labelManageContentProjection{}, errs.InvalidArgument("entity_type", "Label is required")
	}
	var projection labelManageContentProjection
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, load := range loadRoot {
			if load != nil {
				if err := load(ctx, tx); err != nil {
					return err
				}
			}
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
				return errs.NotFound("label_translation", entityID)
			}
			return errs.Internal(err)
		}
		projection = labelManageContentProjection{
			Document: document, Revision: snapshot.Document.Revision.String(),
			SourceTitle: row.Title,
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return projection, err
}

func initializeCreativeContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	sourceLocale string,
	created contentblock.Snapshot,
	input *contentv1.RichTextDocument,
	authorize func(context.Context, *gorm.DB) error,
) (contentblock.Result, error) {
	if entityType != labelContentEntity {
		return contentblock.Result{}, errs.InvalidArgument("entity_type", "Label is required")
	}
	if input == nil {
		return contentblock.Result{}, nil
	}
	if input.GetProfile() != contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT {
		return contentblock.Result{}, errs.InvalidArgument("document.profile", "must be compact")
	}
	if strings.TrimSpace(input.GetSourceLocale()) != sourceLocale {
		return contentblock.Result{}, errs.InvalidArgument("document.source_locale", "must match the server-selected source locale")
	}
	replace, err := contentblock.ReplaceFromRichTextProto(created.Document.ID, created.Document.Revision, input)
	if err != nil {
		return contentblock.Result{}, normalizeLabelContentBlockError(err)
	}
	result, err := store.ReplaceSnapshot(ctx, tx, replace, labelContentCreationFence(entityID, sourceLocale, authorize))
	if err != nil {
		return contentblock.Result{}, normalizeLabelContentBlockError(err)
	}
	return result, nil
}
