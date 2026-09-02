package post

import (
	"context"
	"database/sql"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	PostSourceTitleSQL       = "COALESCE((SELECT pt.title FROM post_translation AS pt WHERE pt.entity_id = post.id AND pt.locale = post.source_locale LIMIT 1), '')"
	PostSourceContentTextSQL = "COALESCE((SELECT string_agg(cbl.localized_data::text, ' ') FROM content_block AS cb JOIN content_block_locale AS cbl ON cbl.block_id = cb.id WHERE cb.document_id = post.content_document_id AND cbl.locale = post.source_locale), '')"
)

type postSourceLocaleDocumentRow struct {
	EntityID  string         `gorm:"column:entity_id"`
	Title     sql.NullString `gorm:"column:title"`
	Summary   sql.NullString `gorm:"column:summary"`
	OgAssetID sql.NullString `gorm:"column:og_asset_id"`
}

func postSourceLocaleDocumentStateFromRow(row postSourceLocaleDocumentRow) *translationLocaleDocumentState {
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
	return state
}

func loadPostSourceLocaleDocumentStates(
	ctx context.Context,
	db *gorm.DB,
	postIDs []string,
) (map[string]*translationLocaleDocumentState, error) {
	normalized := normalizeStringIDs(postIDs)
	if len(normalized) == 0 {
		return map[string]*translationLocaleDocumentState{}, nil
	}

	var rows []postSourceLocaleDocumentRow
	result := db.WithContext(ctx).Raw(
		`SELECT pt.entity_id::text AS entity_id,
		        pt.title,
		        pt.summary,
		        pt.og_asset_id
		 FROM post_translation AS pt
		 JOIN post AS p
		   ON p.id = pt.entity_id AND p.source_locale = pt.locale
		 WHERE pt.entity_id::text IN ?`,
		normalized,
	).Scan(&rows)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}

	states := make(map[string]*translationLocaleDocumentState, len(rows))
	for _, row := range rows {
		states[row.EntityID] = postSourceLocaleDocumentStateFromRow(row)
	}

	return states, nil
}

func loadPostSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) (*translationLocaleDocumentState, error) {
	states, err := loadPostSourceLocaleDocumentStates(ctx, db, []string{postID})
	if err != nil {
		return nil, err
	}
	return states[postID], nil
}

func loadRequiredPostSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) (*translationLocaleDocumentState, error) {
	state, err := loadPostSourceLocaleDocumentState(ctx, db, postID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, errs.NotFound("post_translation", postID)
	}
	return state, nil
}

// lockPostRootForLocaleWrite is the first operation in transactions that may
// touch both post and post_translation rows.
func lockPostRootForLocaleWrite(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Table("post").
		Select("id").
		Where("id = ?", postID).
		Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("post", postID)
		}
		return errs.Internal(err)
	}
	return nil
}

func savePostSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	postID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if err := lockPostRootForLocaleWrite(ctx, db, postID); err != nil {
		return err
	}
	return savePostSourceLocaleDocumentStateAfterRootLock(ctx, db, postID, locale, input)
}

func savePostSourceLocaleDocumentStateAfterRootLock(
	ctx context.Context,
	db *gorm.DB,
	postID string,
	locale string,
	input translationLocaleDocumentSaveInput,
) error {
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO post_translation (
			entity_id, locale, title, summary, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title,
			summary = EXCLUDED.summary,
			updated_at = EXCLUDED.updated_at`,
		postID,
		locale,
		input.Title,
		input.Summary,
		input.Now,
		input.Now,
	).Error; err != nil {
		return err
	}
	return nil
}

func LoadPostSourceLocaleDocumentStatesForPublic(
	ctx context.Context,
	db *gorm.DB,
	postIDs []string,
) (map[string]*translationLocaleDocumentState, error) {
	return loadPostSourceLocaleDocumentStates(ctx, db, postIDs)
}

func LoadPostSourceLocaleDocumentStateForPublic(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) (*translationLocaleDocumentState, error) {
	return loadPostSourceLocaleDocumentState(ctx, db, postID)
}

func overlayPostSourceLocaleDocument(
	post *model.Post,
	state *translationLocaleDocumentState,
) {
	if post == nil || state == nil {
		return
	}

	if state.Title != nil {
		post.Title = *state.Title
	}
	if state.Summary != nil {
		post.Summary = state.Summary
	}
	post.SourceLocaleOgAssetID = state.OgAssetID
}

func OverlayPostSourceLocaleDocumentForPublic(
	post *model.Post,
	state *translationLocaleDocumentState,
) {
	overlayPostSourceLocaleDocument(post, state)
}

func collectManagePostIDs(posts []model.Post) []string {
	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		ids = append(ids, post.ID)
	}
	return ids
}
