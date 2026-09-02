package menu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const menuContentDocumentProfile = "compact"

type menuContentDocumentState struct {
	ID           uuid.UUID
	Revision     uuid.UUID
	SourceLocale string
}

func loadMenuContentDocumentStateFromRoot(
	ctx context.Context,
	db *gorm.DB,
	root model.Menu,
	lock bool,
) (menuContentDocumentState, error) {
	if db == nil {
		return menuContentDocumentState{}, errs.Internal(errors.New("menu content document database is required"))
	}
	documentID, err := parseMenuContentDocumentUUID(root.ContentDocumentID, "content_document_id")
	if err != nil {
		return menuContentDocumentState{}, err
	}
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return menuContentDocumentState{}, errs.FailedPrecondition("Menu source locale is not initialized")
	}

	query := db.WithContext(ctx).Table("content_document").
		Select("profile, revision").Where("id = ?", documentID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var document struct {
		Profile  string `gorm:"column:profile"`
		Revision string `gorm:"column:revision"`
	}
	if err := query.Take(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return menuContentDocumentState{}, errs.FailedPrecondition("Menu content document is missing")
		}
		return menuContentDocumentState{}, errs.Internal(err)
	}
	if strings.TrimSpace(document.Profile) != menuContentDocumentProfile {
		return menuContentDocumentState{}, errs.FailedPrecondition("Menu content document profile is invalid")
	}
	revision, err := parseMenuContentDocumentUUID(document.Revision, "content_document.revision")
	if err != nil {
		return menuContentDocumentState{}, err
	}
	return menuContentDocumentState{ID: documentID, Revision: revision, SourceLocale: sourceLocale}, nil
}

func parseMenuContentDocumentUUID(value, field string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, errs.Internal(fmt.Errorf("invalid Menu %s: %s", field, normalized))
	}
	return parsed, nil
}

func createMenuContentDocument(ctx context.Context, tx *gorm.DB, now time.Time) (uuid.UUID, error) {
	if tx == nil {
		return uuid.Nil, errs.Internal(errors.New("menu content document transaction is required"))
	}
	documentID := uuid.New()
	revision := uuid.New()
	if err := tx.WithContext(ctx).Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		documentID, menuContentDocumentProfile, revision, now.UTC(), now.UTC(),
	).Error; err != nil {
		return uuid.Nil, errs.Internal(err)
	}
	return documentID, nil
}

func advanceMenuContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	menuID string,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	now time.Time,
) (uuid.UUID, error) {
	if tx == nil || documentID == uuid.Nil || expectedRevision == uuid.Nil {
		return uuid.Nil, errs.InvalidArgument(
			"expected_document_revision",
			"Menu content document identity and revision are required",
		)
	}
	nextRevision := uuid.New()
	updated := tx.WithContext(ctx).Exec(
		`UPDATE content_document
		 SET revision = ?, updated_at = ?
		 WHERE id = ? AND revision = ?
		   AND EXISTS (
			 SELECT 1 FROM menu
			 WHERE id = ? AND content_document_id = ?
		   )`,
		nextRevision, now.UTC(), documentID, expectedRevision, menuID, documentID,
	)
	if updated.Error != nil {
		return uuid.Nil, errs.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return uuid.Nil, errs.FailedPrecondition("Menu content document revision changed; reload before saving")
	}
	return nextRevision, nil
}

func deleteMenuContentDocument(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) error {
	if tx == nil || documentID == uuid.Nil {
		return errs.InvalidArgument("content_document_id", "Menu content document is required")
	}
	result := tx.WithContext(ctx).Exec("DELETE FROM content_document WHERE id = ?", documentID)
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Menu content document is missing")
	}
	return nil
}
