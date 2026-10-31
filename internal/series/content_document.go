package series

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const seriesContentDocumentProfile = "compact"

type seriesContentDocumentState struct {
	ID           uuid.UUID
	Revision     uuid.UUID
	SourceLocale string
}

type seriesContentDocumentRoot struct {
	ID                string         `gorm:"column:id"`
	SourceLocale      string         `gorm:"column:source_locale"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
}

type seriesContentDocumentRow struct {
	ID       sql.NullString `gorm:"column:id"`
	Profile  string         `gorm:"column:profile"`
	Revision sql.NullString `gorm:"column:revision"`
}

func loadSeriesContentDocumentState(
	ctx context.Context,
	db *gorm.DB,
	seriesID string,
	lock bool,
) (seriesContentDocumentState, error) {
	if db == nil {
		return seriesContentDocumentState{}, errors.New("series content document database is required")
	}
	query := db.WithContext(ctx).Table("series").
		Select("id, source_locale, content_document_id").
		Where("id = ?", seriesID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var root seriesContentDocumentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return seriesContentDocumentState{}, domainerr.ResourceNotFound("series", seriesID)
		}
		return seriesContentDocumentState{}, err
	}
	if !root.ContentDocumentID.Valid || strings.TrimSpace(root.ContentDocumentID.String) == "" {
		return seriesContentDocumentState{}, domainerr.Precondition("series content document is not initialized")
	}
	documentID, err := parseSeriesContentDocumentUUID(root.ContentDocumentID.String, "content_document_id")
	if err != nil {
		return seriesContentDocumentState{}, err
	}

	documentQuery := db.WithContext(ctx).Table("content_document").
		Select("id, profile, revision").Where("id = ?", documentID)
	if lock {
		documentQuery = documentQuery.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var document seriesContentDocumentRow
	if err := documentQuery.Take(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return seriesContentDocumentState{}, domainerr.Precondition("series content document is missing")
		}
		return seriesContentDocumentState{}, err
	}
	if strings.TrimSpace(document.Profile) != seriesContentDocumentProfile {
		return seriesContentDocumentState{}, domainerr.Precondition("series content document profile is invalid")
	}
	sourceLocale := strings.TrimSpace(root.SourceLocale)
	if sourceLocale == "" {
		return seriesContentDocumentState{}, domainerr.Precondition("series source locale is not initialized")
	}
	revision, err := parseSeriesContentDocumentUUID(document.Revision.String, "content_document.revision")
	if err != nil {
		return seriesContentDocumentState{}, err
	}
	return seriesContentDocumentState{
		ID:           documentID,
		Revision:     revision,
		SourceLocale: sourceLocale,
	}, nil
}

func parseSeriesContentDocumentUUID(value, field string) (uuid.UUID, error) {
	normalized := strings.TrimSpace(value)
	parsed, err := uuid.Parse(normalized)
	if err != nil || parsed == uuid.Nil || parsed.String() != normalized {
		return uuid.Nil, fmt.Errorf("invalid Series %s %q", field, normalized)
	}
	return parsed, nil
}

func createSeriesContentDocument(ctx context.Context, tx *gorm.DB, now time.Time) (uuid.UUID, error) {
	if tx == nil {
		return uuid.Nil, errors.New("series content document transaction is required")
	}
	documentID := uuid.New()
	revision := uuid.New()
	if err := tx.WithContext(ctx).Exec(
		`INSERT INTO content_document (id, profile, revision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		documentID, seriesContentDocumentProfile, revision, now.UTC(), now.UTC(),
	).Error; err != nil {
		return uuid.Nil, err
	}
	return documentID, nil
}

func advanceSeriesContentDocument(
	ctx context.Context,
	tx *gorm.DB,
	seriesID string,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	now time.Time,
) (uuid.UUID, error) {
	if tx == nil || documentID == uuid.Nil || expectedRevision == uuid.Nil {
		return uuid.Nil, domainerr.InvalidField("expected_document_revision", "Series content document identity and revision are required")
	}
	nextRevision := uuid.New()
	updated := tx.WithContext(ctx).Exec(
		`UPDATE content_document
		 SET revision = ?, updated_at = ?
		 WHERE id = ? AND revision = ?
		   AND EXISTS (
			 SELECT 1 FROM series
			 WHERE id = ? AND content_document_id = ?
		   )`,
		nextRevision, now.UTC(), documentID, expectedRevision, seriesID, documentID,
	)
	if updated.Error != nil {
		return uuid.Nil, updated.Error
	}
	if updated.RowsAffected != 1 {
		return uuid.Nil, domainerr.Precondition("series content document revision changed; reload before saving")
	}
	return nextRevision, nil
}

func advanceSeriesContentDocumentForMutation(ctx context.Context, tx *gorm.DB, seriesID string, now time.Time) error {
	state, err := loadSeriesContentDocumentState(ctx, tx, seriesID, false)
	if err != nil {
		return err
	}
	_, err = advanceSeriesContentDocument(ctx, tx, seriesID, state.ID, state.Revision, now)
	return err
}

func deleteSeriesContentDocument(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) error {
	if tx == nil || documentID == uuid.Nil {
		return domainerr.InvalidField("content_document_id", "Series content document is required")
	}
	result := tx.WithContext(ctx).Exec("DELETE FROM content_document WHERE id = ?", documentID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domainerr.Precondition("series content document is missing")
	}
	return nil
}
