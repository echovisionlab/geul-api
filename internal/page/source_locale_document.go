package page

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

func pageSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "page"
	}
	return fmt.Sprintf(
		"(SELECT pt.%s FROM page_translation AS pt WHERE pt.entity_id = %s.id AND pt.locale = %s.source_locale LIMIT 1)",
		column,
		alias,
		alias,
	)
}

func PageSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", pageSourceLocaleColumnSQL(tableAlias, "title"))
}

type pageSourceLocaleMetadataRow struct {
	EntityID  string         `gorm:"column:entity_id"`
	Locale    string         `gorm:"column:locale"`
	Title     sql.NullString `gorm:"column:title"`
	Summary   sql.NullString `gorm:"column:summary"`
	OgAssetID sql.NullString `gorm:"column:og_asset_id"`
}

// PageSourceLocaleMetadata is Page-owned metadata for the source locale.
// Block content and revisions are loaded independently from content_document;
// page_translation is no longer a document body/cache authority.
type PageSourceLocaleMetadata struct {
	Locale    string
	Title     *string
	Summary   *string
	OgAssetID *string
}

func loadPageSourceLocaleMetadataByPageID(
	ctx context.Context,
	db *gorm.DB,
	pageIDs []string,
) (map[string]*PageSourceLocaleMetadata, error) {
	normalized := normalizeStringIDs(pageIDs)
	if len(normalized) == 0 {
		return map[string]*PageSourceLocaleMetadata{}, nil
	}

	var rows []pageSourceLocaleMetadataRow
	result := db.WithContext(ctx).Raw(
		`SELECT CAST(pt.entity_id AS text) AS entity_id,
		        p.source_locale AS locale,
		        pt.title,
		        pt.summary,
		        pt.og_asset_id
		 FROM page AS p
		 JOIN page_translation AS pt
		   ON pt.entity_id = p.id
		  AND pt.locale = p.source_locale
		 WHERE CAST(p.id AS text) IN ?
		`,
		normalized,
	).Scan(&rows)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}

	states := make(map[string]*PageSourceLocaleMetadata, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Locale) == "" {
			return nil, errs.Internal(fmt.Errorf("page %s has an empty source locale", row.EntityID))
		}
		state := &PageSourceLocaleMetadata{
			Locale: row.Locale,
		}
		if row.Title.Valid {
			state.Title = &row.Title.String
		}
		if row.Summary.Valid {
			state.Summary = &row.Summary.String
		}
		if row.OgAssetID.Valid {
			state.OgAssetID = &row.OgAssetID.String
		}
		states[row.EntityID] = state
	}

	return states, nil
}

func loadPageSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (*PageSourceLocaleMetadata, error) {
	states, err := loadPageSourceLocaleMetadataByPageID(ctx, db, []string{pageID})
	if err != nil {
		return nil, err
	}
	return states[pageID], nil
}

func loadRequiredPageSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (*PageSourceLocaleMetadata, error) {
	state, err := loadPageSourceLocaleMetadata(ctx, db, pageID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errs.NotFound("page_translation", pageID)
	}
	return state, nil
}

func createInitialPageSourceLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
	sourceLocale string,
	title string,
	summary *string,
	now time.Time,
) error {
	if strings.TrimSpace(sourceLocale) == "" {
		return errs.Internal(fmt.Errorf("invalid initial Page source locale"))
	}

	if err := db.WithContext(ctx).Exec(`
		INSERT INTO page_translation (
			entity_id,
			locale,
			title,
			summary,
			created_at,
			updated_at
			) VALUES (?, ?, ?, ?, ?, ?)
	`,
		pageID,
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

func overlayPageSourceLocaleMetadata(
	page *model.Page,
	state *PageSourceLocaleMetadata,
) {
	if page == nil || state == nil {
		return
	}

	page.Title = ""
	if state.Title != nil {
		page.Title = *state.Title
	}
	page.Summary = state.Summary
	page.SourceLocaleOgAssetID = state.OgAssetID
}

func LoadPageSourceLocaleMetadataForPublic(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
) (*PageSourceLocaleMetadata, error) {
	return loadPageSourceLocaleMetadata(ctx, db, pageID)
}

func OverlayPageSourceLocaleMetadataForPublic(
	page *model.Page,
	state *PageSourceLocaleMetadata,
) {
	overlayPageSourceLocaleMetadata(page, state)
}

func collectManagePageIDs(pages []model.Page) []string {
	ids := make([]string, 0, len(pages))
	for _, page := range pages {
		ids = append(ids, page.ID)
	}
	return ids
}
