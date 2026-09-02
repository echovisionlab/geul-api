package work

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
)

type workSourceLocaleMetadataRow struct {
	EntityID string         `gorm:"column:entity_id"`
	Locale   string         `gorm:"column:locale"`
	Title    sql.NullString `gorm:"column:title"`
	Summary  sql.NullString `gorm:"column:summary"`
}

type workSourceLocaleMetadata struct {
	Locale  string
	Title   *string
	Summary *string
}

func loadWorkSourceLocaleMetadataByWorkID(
	ctx context.Context,
	db *gorm.DB,
	workIDs []string,
) (map[string]*workSourceLocaleMetadata, error) {
	normalized := normalizeStringIDs(workIDs)
	if len(normalized) == 0 {
		return map[string]*workSourceLocaleMetadata{}, nil
	}

	var rows []workSourceLocaleMetadataRow
	result := db.WithContext(ctx).Raw(`
		SELECT wt.entity_id::text AS entity_id,
		       wt.locale,
		       wt.title,
		       wt.summary
		FROM work_translation AS wt
		JOIN work AS w ON w.id = wt.entity_id
		 AND w.source_locale = wt.locale
		WHERE wt.entity_id::text IN ?
	`, normalized).Scan(&rows)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}

	states := make(map[string]*workSourceLocaleMetadata, len(rows))
	for _, row := range rows {
		state := &workSourceLocaleMetadata{
			Locale: row.Locale,
		}
		if row.Title.Valid {
			state.Title = &row.Title.String
		}
		if row.Summary.Valid {
			state.Summary = &row.Summary.String
		}
		states[row.EntityID] = state
	}
	return states, nil
}

// LoadRequiredSourceLocaleMetadata loads the Work-owned source-locale row.
func LoadRequiredSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	workID string,
) (*workSourceLocaleMetadata, error) {
	states, err := loadWorkSourceLocaleMetadataByWorkID(ctx, db, []string{workID})
	if err != nil {
		return nil, err
	}
	state := states[workID]
	if state == nil {
		return nil, errs.NotFound("work_translation", workID)
	}
	return state, nil
}

func createInitialWorkSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	workID string,
	sourceLocale string,
	title string,
	summary *string,
	now time.Time,
) error {
	if strings.TrimSpace(sourceLocale) == "" {
		return errs.InternalMsg("invalid initial Work source locale")
	}

	if err := db.WithContext(ctx).Exec(`
		INSERT INTO work_translation (
			entity_id,
			locale,
			title,
			summary,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`,
		workID,
		sourceLocale,
		title,
		summary,
		now,
		now,
	).Error; err != nil {
		return errs.Internal(err)
	}
	return nil
}
