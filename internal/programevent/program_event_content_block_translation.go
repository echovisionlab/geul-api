package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type programEventSourceState struct {
	SourceLocale string
}

func loadProgramEventSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	lock bool,
) (programEventSourceState, error) {
	if db == nil {
		return programEventSourceState{}, errs.Internal(errors.New("program Event translation source database is required"))
	}
	query := db.WithContext(ctx).Table("program_event").
		Select("source_locale").Where("id = ?", eventID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var state programEventSourceState
	if err := query.Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programEventSourceState{}, errs.FailedPrecondition("Program Event translation source is not initialized")
		}
		return programEventSourceState{}, errs.Internal(err)
	}
	if strings.TrimSpace(state.SourceLocale) == "" {
		return programEventSourceState{}, errs.Internal(fmt.Errorf("program Event %s has invalid source locale", eventID))
	}
	return state, nil
}

type programEventTranslationSourceMetadata struct {
	Title   string
	Summary *string
}

func loadTranslationSourceMetadata(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	sourceLocale string,
) (programEventTranslationSourceMetadata, error) {
	var row struct {
		Title   string  `gorm:"column:title"`
		Summary *string `gorm:"column:summary"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT program_event.title, translation.summary
		 FROM program_event
		 JOIN program_event_translation AS translation
		   ON translation.entity_id = program_event.id
		  AND translation.locale = ?
		 WHERE program_event.id = ?
		 LIMIT 1`,
		sourceLocale,
		eventID,
	).Scan(&row)
	if result.Error != nil {
		return programEventTranslationSourceMetadata{}, errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return programEventTranslationSourceMetadata{}, errs.FailedPrecondition("Program Event source locale metadata is not initialized")
	}
	return programEventTranslationSourceMetadata{Title: row.Title, Summary: row.Summary}, nil
}

func loadProgramEventBlockLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	sourceLocale string,
) (*intrav1.ProgramEventLocaleMetadata, error) {
	var root struct {
		Title string `gorm:"column:title"`
	}
	if err := db.WithContext(ctx).
		Table("program_event").
		Select("title").
		Where("id = ?", eventID).
		Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.NotFound("program event", eventID)
		}
		return nil, errs.Internal(err)
	}
	var locale struct {
		Summary *string `gorm:"column:summary"`
	}
	if err := db.WithContext(ctx).
		Table("program_event_translation").
		Select("summary").
		Where("entity_id = ? AND locale = ?", eventID, sourceLocale).
		Take(&locale).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.FailedPrecondition("Program Event source locale metadata is not initialized")
		}
		return nil, errs.Internal(err)
	}
	title := root.Title
	return &intrav1.ProgramEventLocaleMetadata{
		Locale:  sourceLocale,
		Title:   &title,
		Summary: locale.Summary,
	}, nil
}
