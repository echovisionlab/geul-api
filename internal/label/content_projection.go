package label

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
			return labelSourceAuthority{}, domainerr.ResourceNotFound("label", labelID)
		}
		return labelSourceAuthority{}, err
	}
	return state, nil
}

func loadLabelLocaleTitles(ctx context.Context, db *gorm.DB, labelID string) ([]labelLocaleTitle, error) {
	var rows []labelLocaleTitle
	if err := db.WithContext(ctx).Table("label_translation").Select("locale", "title", "updated_at").Where("entity_id = ?", labelID).Order("locale ASC").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func loadLabelContentSnapshot(ctx context.Context, tx *gorm.DB, store *contentblock.Store, labelID string) (contentblock.Snapshot, labelSourceAuthority, error) {
	var row struct {
		DocumentID *uuid.UUID `gorm:"column:content_document_id"`
	}
	if err := tx.WithContext(ctx).Table("label").Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).Select("content_document_id").Where("id = ?", labelID).Take(&row).Error; err != nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, err
	}
	if row.DocumentID == nil || *row.DocumentID == uuid.Nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, domainerr.Precondition("label content document is not initialized")
	}
	source, err := loadLabelSourceAuthority(ctx, tx, labelID, clause.LockingStrengthShare)
	if err != nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, *row.DocumentID, source.SourceLocale)
	if err != nil {
		return contentblock.Snapshot{}, labelSourceAuthority{}, normalizeLabelContentBlockError(err)
	}
	if strings.TrimSpace(snapshot.SourceLocale) != source.SourceLocale {
		return contentblock.Snapshot{}, labelSourceAuthority{}, errors.New("label content document source locale does not match label source locale")
	}
	return snapshot, source, nil
}

type labelContentBootstrap struct {
	Snapshot contentblock.Snapshot
	Source   labelSourceAuthority
}

// loadLabelContentBlockBootstrap resolves the collaboration session's
// principal, requires Edit, and reads the Label's document and loadMetadata
// in one snapshot.
func loadLabelContentBlockBootstrap(ctx context.Context, db *gorm.DB, store *contentblock.Store, spiceDB *auth.SpiceDBClient, labelID string, sessionID string, loadMetadata func(context.Context, *gorm.DB) error) (labelContentBootstrap, error) {
	if sessionID == "" {
		return labelContentBootstrap{}, errAuthenticationRequired
	}
	var output labelContentBootstrap
	err := mutation.ReadLocked(ctx, db, func(tx *gorm.DB) error {
		principal, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, tx, sessionID)
		if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
			return errAuthenticationRequired
		}
		if err != nil {
			return fmt.Errorf("resolve Label collaboration principal: %w", err)
		}
		if principal == nil || !principal.Authenticated {
			return errAuthenticationRequired
		}
		if principal.Banned {
			return domainerr.Denied("account is banned")
		}
		if !principal.Onboarded {
			return domainerr.Denied("no permission to edit this label")
		}
		if err := requireLabelCollaborationEdit(ctx, spiceDB, labelID, principal); err != nil {
			return err
		}
		snapshot, source, err := loadLabelContentSnapshot(ctx, tx, store, labelID)
		if err != nil {
			return err
		}
		if err := loadMetadata(ctx, tx); err != nil {
			return err
		}
		output = labelContentBootstrap{Snapshot: snapshot, Source: source}
		return nil
	})
	return output, err
}
