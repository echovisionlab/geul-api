package label

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type labelSourceAuthority struct {
	SourceLocale string `gorm:"column:source_locale"`
}

type labelLocaleTitle struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func loadLabelSourceAuthority(ctx context.Context, db *gorm.DB, labelID string, lock string) (labelSourceAuthority, error) {
	var state labelSourceAuthority
	query := db.WithContext(ctx).Table("label").Select("source_locale").Where("id = ?", labelID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return labelSourceAuthority{}, errs.NotFound("label", labelID)
		}
		return labelSourceAuthority{}, errs.Internal(err)
	}
	return state, nil
}

func loadLabelLocaleTitles(ctx context.Context, db *gorm.DB, labelID string) ([]labelLocaleTitle, error) {
	var rows []labelLocaleTitle
	if err := db.WithContext(ctx).Table("label_translation").Select("locale", "title", "updated_at").Where("entity_id = ?", labelID).Order("locale ASC").Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	return rows, nil
}

func loadLabelContentSnapshot(ctx context.Context, tx *gorm.DB, store *contentblock.Store, labelID string) (contentblock.Snapshot, labelSourceAuthority, error) {
	if store == nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, errs.Internal(errors.New("label content Block store is not configured"))
	}
	var row struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	if err := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: "SHARE"}).Select("content_document_id").Where("id = ?", labelID).Take(&row).Error; err != nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, errs.Internal(err)
	}
	if row.DocumentID == nil || *row.DocumentID == uuid.Nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, errs.FailedPrecondition("label content document is not initialized")
	}
	source, err := loadLabelSourceAuthority(ctx, tx, labelID, "SHARE")
	if err != nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, *row.DocumentID, source.SourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, normalizeLabelContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != source.SourceLocale {
		return contentblock.Snapshot{}, labelSourceAuthority{}, errs.Internal(errors.New("label content document source locale does not match label source locale"))
	}
	return snapshot, source, nil
}

type labelContentBootstrap struct {
	Snapshot contentblock.Snapshot
	Source   labelSourceAuthority
}

func loadLabelContentBlockBootstrap(ctx context.Context, db *gorm.DB, store *contentblock.Store, spiceDB *auth.SpiceDBClient, labelID string, resourceType intrav1.CollaborationResourceType, principalMessage *intrav1.CollaborationPrincipal, loadMetadata func(context.Context, *gorm.DB) error) (labelContentBootstrap, error) {
	if principalMessage == nil {
		return labelContentBootstrap{}, errs.AuthenticationRequired()
	}
	if store == nil {
		return labelContentBootstrap{}, errs.Internal(errors.New("label content Block store is not configured"))
	}
	var output labelContentBootstrap
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, tx, principalMessage.GetSessionId())
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return errs.AuthenticationRequired()
		}
		if err != nil {
			return errs.Internal(fmt.Errorf("resolve Label collaboration principal: %w", err))
		}
		if principal == nil || !principal.Authenticated {
			return errs.AuthenticationRequired()
		}
		if principal.Banned {
			return errs.AccountBanned()
		}
		if !principal.Onboarded {
			return errs.NoPermission("edit", "label")
		}
		if err := requireLabelCollaborationEdit(ctx, spiceDB, resourceType, labelID, principal); err != nil {
			return err
		}
		snapshot, source, err := loadLabelContentSnapshot(ctx, tx, store, labelID)
		if err != nil {
			return err
		}
		if loadMetadata != nil {
			if err := loadMetadata(ctx, tx); err != nil {
				return err
			}
		}
		output = labelContentBootstrap{Snapshot: snapshot, Source: source}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return output, err
}
