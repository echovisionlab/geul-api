package form

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

type formDocumentSnapshot struct {
	state     *TranslationDocumentState
	updatedAt time.Time
}

type FormSourceDocument struct {
	Title       string
	Schema      []byte
	ContentText *string
}

func defaultFormTitle() string {
	return "Untitled Form"
}

func formSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "form"
	}
	return fmt.Sprintf(
		"(SELECT ft.%s FROM form_translation AS ft "+
			"WHERE ft.entity_id = %s.id AND ft.locale = %s.source_locale LIMIT 1)",
		column,
		alias,
		alias,
	)
}

func FormSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf(
		"COALESCE(NULLIF(BTRIM(%s), ''), '%s')",
		formSourceLocaleColumnSQL(tableAlias, "title"),
		defaultFormTitle(),
	)
}

func resolveFormTitle(title *string) string {
	if title != nil && strings.TrimSpace(*title) != "" {
		return *title
	}
	return defaultFormTitle()
}

func loadFormTranslationStoredSnapshot(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	locale string,
) (*formDocumentSnapshot, error) {
	var row struct {
		Title       sql.NullString `gorm:"column:title"`
		ContentJSON []byte         `gorm:"column:content_json"`
		ContentText sql.NullString `gorm:"column:content_text"`
		OgAssetID   sql.NullString `gorm:"column:og_asset_id"`
		UpdatedAt   time.Time      `gorm:"column:updated_at"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT title, content_json, content_text, og_asset_id, updated_at
		 FROM form_translation
		 WHERE entity_id = ? AND locale = ?
		 LIMIT 1`,
		formID,
		locale,
	).Scan(&row)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, errs.NotFound("form translation", formID+":"+locale)
	}

	state := &TranslationDocumentState{
		ContentJSON: row.ContentJSON,
	}
	if row.Title.Valid {
		state.Title = &row.Title.String
	}
	if row.ContentText.Valid {
		state.ContentText = &row.ContentText.String
	}
	if row.OgAssetID.Valid {
		state.OgAssetID = &row.OgAssetID.String
	}

	return &formDocumentSnapshot{
		state:     state,
		updatedAt: row.UpdatedAt,
	}, nil
}

func loadFormResolvedSourceLocale(
	ctx context.Context,
	db *gorm.DB,
	formID string,
) (string, error) {
	var sourceRow struct {
		Locale string `gorm:"column:locale"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT source_locale AS locale
		 FROM form
		 WHERE id = ?
		 LIMIT 1`,
		formID,
	).Scan(&sourceRow)
	if result.Error != nil {
		return "", errs.Internal(result.Error)
	}
	if result.RowsAffected == 0 || strings.TrimSpace(sourceRow.Locale) == "" {
		return "", errs.NotFound("form source translation", formID)
	}
	return sourceRow.Locale, nil
}

func LoadFormCanonicalSourceDocumentState(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	sourceLocale string,
) (*TranslationDocumentState, error) {
	sourceLocale = strings.TrimSpace(sourceLocale)
	if sourceLocale == "" {
		return nil, errs.NotFound("form source translation", formID)
	}

	snapshot, err := loadFormTranslationStoredSnapshot(ctx, db, formID, sourceLocale)
	if err != nil {
		return nil, err
	}
	return snapshot.state, nil
}

func LoadCurrentFormSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	formID string,
) (*TranslationDocumentState, error) {
	sourceLocale, err := loadFormResolvedSourceLocale(ctx, db, formID)
	if err != nil {
		return nil, err
	}
	return LoadFormCanonicalSourceDocumentState(ctx, db, formID, sourceLocale)
}

// PrepareSourceLocaleSwitch ensures that a requested Form source locale has a
// canonical row before the shared Content Document pointer is switched. A
// missing locale starts as an empty localized overlay over the current source
// topology; an existing locale is preserved exactly and must already match
// that topology.
func PrepareSourceLocaleSwitch(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	currentSourceLocale string,
	requestedLocale string,
	now time.Time,
) error {
	if db == nil || !IsValidUUID(formID) {
		return errs.InvalidArgument("form_id", "must be a canonical UUID")
	}
	currentSourceLocale = strings.TrimSpace(currentSourceLocale)
	requestedLocale = strings.TrimSpace(requestedLocale)
	if currentSourceLocale == "" || requestedLocale == "" ||
		currentSourceLocale == requestedLocale {
		return errs.InvalidArgument(
			"source_locale",
			"current and requested Form source locales are required and must differ",
		)
	}
	if now.IsZero() {
		return errs.InvalidArgument("now", "Form source-locale switch time is required")
	}
	root, err := loadFormAIDocumentRoot(ctx, db, formID, "")
	if err != nil {
		return err
	}
	if root.SourceLocale != currentSourceLocale {
		return errs.FailedPrecondition("Form source locale changed")
	}
	source, sourceExists, err := loadFormAIDocumentLocale(
		ctx, db, formID, currentSourceLocale, true,
	)
	if err != nil {
		return err
	}
	if !sourceExists {
		return errs.FailedPrecondition("Form source locale document is missing")
	}
	if err := validateCanonicalFormSchema(source.Schema); err != nil {
		return errs.FailedPrecondition("Form source locale document is invalid")
	}
	requested, requestedExists, err := loadFormAIDocumentLocale(
		ctx, db, formID, requestedLocale, true,
	)
	if err != nil {
		return err
	}
	if requestedExists {
		if err := validateFormAIDocumentTargetSchema(source.Schema, requested.Schema); err != nil {
			return errs.FailedPrecondition("Form requested source locale document is invalid")
		}
		return nil
	}
	emptySchema, err := formAIDocumentEmptyTargetSchema(source.Schema)
	if err != nil {
		return errs.FailedPrecondition("Form source locale document is invalid")
	}
	writeTime := now.UTC()
	result := db.WithContext(ctx).Exec(
		`INSERT INTO form_translation (
			entity_id, locale, title, content_json, content_text, created_at, updated_at
		) VALUES (?::uuid, ?, NULL, CAST(? AS jsonb), NULL, ?, ?)
		ON CONFLICT (entity_id, locale) DO NOTHING`,
		formID,
		requestedLocale,
		string(emptySchema),
		writeTime,
		writeTime,
	)
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return errs.FailedPrecondition("Form requested source locale changed")
	}
	return nil
}

func saveFormSourceLocaleDocumentState(
	ctx context.Context,
	db *gorm.DB,
	formID string,
	locale string,
	input TranslationDocumentSaveInput,
) error {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if len(input.ContentJSON) == 0 {
		existingSnapshot, err := loadFormTranslationStoredSnapshot(ctx, db, formID, locale)
		if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			return err
		}
		if existingSnapshot != nil && len(input.ContentJSON) == 0 {
			input.ContentJSON = existingSnapshot.state.ContentJSON
		}
	}

	var titleValue structured.Value
	if input.Title != nil {
		titleValue = *input.Title
	}
	schemaJSONValue := structured.Value(nil)
	if input.ContentJSON != nil {
		schemaJSONValue = input.ContentJSON
	}
	contentText := formCanonicalContentText(input.ContentJSON)
	if err := db.WithContext(ctx).Exec(
		`INSERT INTO form_translation (
			entity_id, locale, title, content_json, content_text,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = COALESCE(EXCLUDED.title, form_translation.title),
			content_json = COALESCE(EXCLUDED.content_json, form_translation.content_json),
			content_text = EXCLUDED.content_text,
			updated_at = EXCLUDED.updated_at`,
		formID,
		locale,
		titleValue,
		schemaJSONValue,
		contentText,
		now,
		now,
	).Error; err != nil {
		return errs.Internal(err)
	}

	return nil
}

func createInitialFormSourceLocaleRow(
	ctx context.Context,
	db *gorm.DB,
	translation Translation,
	formID string,
	sourceLocale string,
	title string,
	schemaJSON []byte,
	now time.Time,
) error {
	sourceLocale = translation.NormalizeInitialSourceLocale(ctx, db, sourceLocale)

	return saveFormSourceLocaleDocumentState(ctx, db, formID, sourceLocale, TranslationDocumentSaveInput{
		Title:       &title,
		ContentJSON: schemaJSON,
		Now:         now,
	})
}

func loadFormSourceSchema(
	ctx context.Context,
	db *gorm.DB,
	formID string,
) ([]byte, *string, error) {
	state, err := LoadCurrentFormSourceLocaleDocumentState(ctx, db, formID)
	if err != nil {
		return nil, nil, err
	}
	return state.ContentJSON, state.Title, nil
}

func loadFormSourceTitles(
	ctx context.Context,
	db *gorm.DB,
	formIDs []string,
) (map[string]string, error) {
	if len(formIDs) == 0 {
		return map[string]string{}, nil
	}

	var rows []struct {
		EntityID string         `gorm:"column:entity_id"`
		Title    sql.NullString `gorm:"column:title"`
	}

	result := db.WithContext(ctx).Raw(
		`SELECT ft.entity_id, ft.title
		 FROM form_translation AS ft
		 JOIN form AS root ON root.id = ft.entity_id AND root.source_locale = ft.locale
		 WHERE ft.entity_id IN ?`,
		formIDs,
	).Scan(&rows)
	if result.Error != nil {
		return nil, errs.Internal(result.Error)
	}

	titles := make(map[string]string, len(rows))
	for _, formID := range formIDs {
		titles[formID] = defaultFormTitle()
	}
	for _, row := range rows {
		if row.Title.Valid {
			title := strings.TrimSpace(row.Title.String)
			if title != "" {
				titles[row.EntityID] = title
				continue
			}
		}
		titles[row.EntityID] = defaultFormTitle()
	}
	return titles, nil
}

func LoadCurrentFormSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	formID string,
) (*FormSourceDocument, error) {
	state, err := LoadCurrentFormSourceLocaleDocumentState(ctx, db, formID)
	if err != nil {
		return nil, err
	}

	return &FormSourceDocument{
		Title:       resolveFormTitle(state.Title),
		Schema:      state.ContentJSON,
		ContentText: state.ContentText,
	}, nil
}
