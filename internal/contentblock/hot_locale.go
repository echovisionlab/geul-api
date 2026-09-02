package contentblock

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type hotLocaleInput struct {
	BlockID       uuid.UUID       `json:"block_id"`
	Locale        string          `json:"locale"`
	ExpectedKind  string          `json:"expected_kind"`
	Delete        bool            `json:"delete"`
	LocalizedData json.RawMessage `json:"localized_data,omitempty"`
}

type hotLocaleTarget struct {
	BlockID       uuid.UUID       `json:"block_id"`
	Locale        string          `json:"locale"`
	Delete        bool            `json:"delete"`
	LocalizedData json.RawMessage `json:"localized_data,omitempty"`
	Changed       bool            `json:"changed"`
}

type hotLocaleResultRow struct {
	Status       string          `gorm:"column:status"`
	Profile      string          `gorm:"column:profile"`
	Revision     uuid.UUID       `gorm:"column:revision"`
	CreatedAt    time.Time       `gorm:"column:created_at"`
	UpdatedAt    time.Time       `gorm:"column:updated_at"`
	ChangedCount int             `gorm:"column:changed_count"`
	Targets      json.RawMessage `gorm:"column:targets"`
}

// applyLocaleBatchPostgres applies a locale-only mutation with one PostgreSQL
// statement. The CTE owns document CAS, Block ownership checks, and all locale
// row writes. It returns only the affected base rows needed to run the durable
// generated payload validator before the caller commits the transaction.
func (s *Store) applyLocaleBatchPostgres(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
) (Result, error) {
	inputs, err := s.normalizeHotLocaleInputs(batch, domain.SourceLocale)
	if err != nil {
		return Result{}, err
	}
	payload, err := json.Marshal(inputs)
	if err != nil {
		return Result{}, fmt.Errorf("encode locale mutation: %w", err)
	}

	newRevision := s.newUUID()
	updatedAt := s.now()
	var row hotLocaleResultRow
	err = tx.WithContext(ctx).Raw(hotLocaleMutationSQL,
		string(payload),
		batch.DocumentID,
		batch.DocumentID,
		batch.DocumentID,
		newRevision,
		updatedAt,
		batch.DocumentID,
		batch.ExpectedRevision,
		batch.validatedProfile,
		batch.ExpectedRevision,
		batch.validatedProfile,
		batch.DocumentID,
	).Scan(&row).Error
	if err != nil {
		return Result{}, fmt.Errorf("apply content Block locale mutation: %w", err)
	}

	switch row.Status {
	case "not_found":
		return Result{}, ErrDocumentNotFound
	case "stale":
		return Result{}, staleRevision(row.Revision)
	case "cross_document":
		return Result{}, ErrCrossDocument
	case "missing_block":
		return Result{}, fmt.Errorf("%w: locale mutation Block does not exist", ErrInvalidMutation)
	case "invalid_profile":
		return Result{}, fmt.Errorf("%w: mutation profile does not match document", ErrInvalidMutation)
	case "invalid_kind":
		return Result{}, fmt.Errorf("%w: locale payload kind does not match stored Block", ErrInvalidMutation)
	case "noop", "applied":
	default:
		return Result{}, fmt.Errorf("%w: unexpected locale mutation status %q", ErrInvalidMutation, row.Status)
	}

	var targets []hotLocaleTarget
	if err := json.Unmarshal(row.Targets, &targets); err != nil {
		return Result{}, fmt.Errorf("%w: decode affected locale Blocks: %v", ErrInvalidMutation, err)
	}
	if row.Status == "noop" {
		return Result{
			DocumentRevision: row.Revision,
		}, nil
	}
	changedLocales := changedHotLocales(targets)
	sourceChanged := containsLocale(changedLocales, domain.SourceLocale)
	return Result{
		DocumentRevision:         row.Revision,
		Changed:                  true,
		ContentChanged:           true,
		TranslationSourceChanged: sourceChanged,
		ChangedLocales:           changedLocales,
	}, nil
}

func (s *Store) normalizeHotLocaleInputs(batch Batch, sourceLocale string) ([]hotLocaleInput, error) {
	if err := validateOperationIDs(batch); err != nil {
		return nil, err
	}
	inputs := make([]hotLocaleInput, 0)
	for _, group := range batch.LocaleGroups {
		for _, update := range group.Upserts {
			if update.ExpectedKind == "" {
				return nil, fmt.Errorf("%w: Block %s locale %s expected kind is required", ErrInvalidMutation, update.BlockID, group.Locale)
			}
			localized, err := s.contract.ValidateLocale(batch.validatedProfile, update.ExpectedKind, update.LocalizedData)
			if err != nil {
				return nil, fmt.Errorf("%w: Block %s locale %s payload: %v", ErrInvalidMutation, update.BlockID, group.Locale, err)
			}
			inputs = append(inputs, hotLocaleInput{
				BlockID: update.BlockID, Locale: group.Locale, ExpectedKind: update.ExpectedKind, LocalizedData: localized,
			})
		}
		for _, blockID := range group.Deletes {
			if group.Locale == sourceLocale {
				return nil, fmt.Errorf("%w: source locale Block overlay cannot be deleted", ErrInvalidMutation)
			}
			inputs = append(inputs, hotLocaleInput{BlockID: blockID, Locale: group.Locale, Delete: true})
		}
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("%w: empty locale mutation batch", ErrInvalidMutation)
	}
	return inputs, nil
}

func changedHotLocales(targets []hotLocaleTarget) []string {
	seen := make(map[string]struct{})
	for _, target := range targets {
		if target.Changed {
			seen[target.Locale] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for locale := range seen {
		result = append(result, locale)
	}
	sort.Strings(result)
	return result
}

func containsLocale(locales []string, locale string) bool {
	index := sort.SearchStrings(locales, locale)
	return index < len(locales) && locales[index] == locale
}

func isPostgres(tx *gorm.DB) bool {
	return tx != nil && tx.Dialector != nil && tx.Dialector.Name() == "postgres"
}

func isLocaleOnlyBatch(batch Batch) bool {
	return batch.validatedProfile != "" && len(batch.LocaleGroups) > 0 && len(batch.Upserts) == 0 && len(batch.Deletes) == 0 && len(batch.Reorders) == 0
}

const hotLocaleMutationSQL = `
WITH input AS MATERIALIZED (
    SELECT value.block_id,
           value.locale,
	       value.expected_kind,
           value.delete,
           value.localized_data
    FROM jsonb_to_recordset(?::jsonb) AS value(
        block_id uuid,
        locale text,
        expected_kind text,
        delete boolean,
        localized_data jsonb
    )
),
document AS MATERIALIZED (
    SELECT id, profile, revision, created_at, updated_at
    FROM content_document
    WHERE id = ?::uuid
),
targets AS MATERIALIZED (
    SELECT input.block_id,
           input.locale,
           input.expected_kind,
           input.delete,
           input.localized_data,
           block.id AS owned_block_id,
           block.document_id AS actual_document_id,
           block.kind,
           locale_row.localized_data AS previous_localized_data
    FROM input
    LEFT JOIN content_block AS block ON block.id = input.block_id
    LEFT JOIN content_block_locale AS locale_row
      ON locale_row.block_id = input.block_id
     AND locale_row.locale = input.locale
),
assessment AS MATERIALIZED (
    SELECT count(*) FILTER (
               WHERE targets.owned_block_id IS NOT NULL
                 AND targets.actual_document_id <> ?::uuid
           ) AS cross_document_count,
           count(*) FILTER (WHERE targets.owned_block_id IS NULL) AS missing_block_count,
           count(*) FILTER (
               WHERE NOT targets.delete
                 AND targets.owned_block_id IS NOT NULL
                 AND targets.kind <> targets.expected_kind
           ) AS kind_mismatch_count,
           count(*) FILTER (
               WHERE targets.actual_document_id = ?::uuid
                 AND (
                     (targets.delete AND targets.previous_localized_data IS NOT NULL)
                     OR
                     (NOT targets.delete AND targets.previous_localized_data IS DISTINCT FROM targets.localized_data)
                 )
           ) AS changed_count
    FROM targets
),
bumped AS (
    UPDATE content_document AS root
       SET revision = ?::uuid,
           updated_at = ?::timestamptz
     WHERE root.id = ?::uuid
       AND root.revision = ?::uuid
       AND root.profile = ?::text
       AND (SELECT cross_document_count FROM assessment) = 0
       AND (SELECT missing_block_count FROM assessment) = 0
       AND (SELECT kind_mismatch_count FROM assessment) = 0
       AND (SELECT changed_count FROM assessment) > 0
    RETURNING root.id, root.profile, root.revision, root.created_at, root.updated_at
),
deleted AS (
    DELETE FROM content_block_locale AS locale_row
    USING targets, bumped
    WHERE targets.delete
      AND targets.actual_document_id = bumped.id
      AND locale_row.block_id = targets.block_id
      AND locale_row.locale = targets.locale
      AND targets.previous_localized_data IS NOT NULL
    RETURNING locale_row.block_id
),
upserted AS (
    INSERT INTO content_block_locale(block_id, locale, localized_data)
    SELECT targets.block_id, targets.locale, targets.localized_data
    FROM targets
    JOIN bumped ON bumped.id = targets.actual_document_id
    WHERE NOT targets.delete
      AND targets.previous_localized_data IS DISTINCT FROM targets.localized_data
    ON CONFLICT (block_id, locale) DO UPDATE
       SET localized_data = EXCLUDED.localized_data
    RETURNING block_id
)
SELECT CASE
           WHEN document.id IS NULL THEN 'not_found'
           WHEN document.revision <> ?::uuid THEN 'stale'
           WHEN document.profile <> ?::text THEN 'invalid_profile'
           WHEN assessment.cross_document_count > 0 THEN 'cross_document'
           WHEN assessment.missing_block_count > 0 THEN 'missing_block'
           WHEN assessment.kind_mismatch_count > 0 THEN 'invalid_kind'
           WHEN assessment.changed_count = 0 THEN 'noop'
           WHEN bumped.id IS NOT NULL THEN 'applied'
           ELSE 'stale'
       END AS status,
       document.profile,
       COALESCE(bumped.revision, document.revision) AS revision,
       document.created_at,
       COALESCE(bumped.updated_at, document.updated_at) AS updated_at,
       assessment.changed_count,
       COALESCE((
           SELECT jsonb_agg(jsonb_build_object(
               'block_id', targets.block_id,
               'locale', targets.locale,
               'delete', targets.delete,
               'changed', targets.actual_document_id = ?::uuid AND (
                   (targets.delete AND targets.previous_localized_data IS NOT NULL)
                   OR
                   (NOT targets.delete AND targets.previous_localized_data IS DISTINCT FROM targets.localized_data)
               ),
               'localized_data', targets.localized_data
           ) ORDER BY targets.locale, targets.block_id)
           FROM targets
       ), '[]'::jsonb) AS targets
FROM (SELECT 1) AS singleton
LEFT JOIN document ON true
CROSS JOIN assessment
LEFT JOIN bumped ON true`
