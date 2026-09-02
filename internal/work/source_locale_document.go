package work

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

type translationLocaleDocumentState struct {
	Title       *string
	Summary     *string
	ContentJSON []byte
	ContentHTML *string
	ContentText *string
	OgAssetID   *string
}

func workSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "work"
	}
	return fmt.Sprintf(
		"(SELECT wt.%s FROM work_translation AS wt JOIN work AS source ON source.id = wt.entity_id AND source.source_locale = wt.locale WHERE wt.entity_id = %s.id LIMIT 1)",
		column,
		alias,
	)
}

func WorkSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", workSourceLocaleColumnSQL(tableAlias, "title"))
}

type workSourceLocaleDocumentRow struct {
	EntityID  string         `gorm:"column:entity_id"`
	Title     sql.NullString `gorm:"column:title"`
	Summary   sql.NullString `gorm:"column:summary"`
	OgAssetID sql.NullString `gorm:"column:og_asset_id"`
}

func loadWorkSourceLocaleDocumentStates(
	ctx context.Context,
	db *gorm.DB,
	workIDs []string,
) (map[string]*translationLocaleDocumentState, error) {
	normalized := normalizeStringIDs(workIDs)
	if len(normalized) == 0 {
		return map[string]*translationLocaleDocumentState{}, nil
	}

	var rows []workSourceLocaleDocumentRow
	result := db.WithContext(ctx).Raw(
		`SELECT wt.entity_id::text AS entity_id,
		        wt.title,
		        wt.summary,
		        wt.og_asset_id::text AS og_asset_id
		 FROM work_translation AS wt
		 JOIN work AS source
		   ON source.id = wt.entity_id AND source.source_locale = wt.locale
		 WHERE wt.entity_id::text IN ?`,
		normalized,
	).Scan(&rows)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}

	states := make(map[string]*translationLocaleDocumentState, len(rows))
	for _, row := range rows {
		state := &translationLocaleDocumentState{}
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

func loadWorkSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	workID string,
) (*translationLocaleDocumentState, error) {
	states, err := loadWorkSourceLocaleDocumentStates(ctx, db, []string{workID})
	if err != nil {
		return nil, err
	}
	return states[workID], nil
}

func overlayWorkSourceLocaleDocument(
	work *model.Work,
	state *translationLocaleDocumentState,
) {
	if work == nil || state == nil {
		return
	}

	if state.Title != nil {
		work.Title = *state.Title
	}
	if state.Summary != nil {
		work.Summary = state.Summary
	}
	work.OgAssetID = state.OgAssetID
}

func LoadWorkSourceLocaleDocumentStatesForPublic(
	ctx context.Context,
	db *gorm.DB,
	workIDs []string,
) (map[string]*translationLocaleDocumentState, error) {
	return loadWorkSourceLocaleDocumentStates(ctx, db, workIDs)
}

func LoadWorkSourceLocaleDocumentStateForPublic(
	ctx context.Context,
	db *gorm.DB,
	workID string,
) (*translationLocaleDocumentState, error) {
	return loadWorkSourceLocaleDocumentState(ctx, db, workID)
}

func OverlayWorkSourceLocaleDocumentForPublic(
	work *model.Work,
	state *translationLocaleDocumentState,
) {
	overlayWorkSourceLocaleDocument(work, state)
}

func collectManageWorkIDs(works []model.Work) []string {
	ids := make([]string, 0, len(works))
	for _, work := range works {
		ids = append(ids, work.ID)
	}
	return ids
}
