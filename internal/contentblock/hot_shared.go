package contentblock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type hotSharedInput struct {
	BlockID       uuid.UUID             `json:"block_id"`
	ParentBlockID *uuid.UUID            `json:"parent_block_id"`
	ContainerSlot string                `json:"container_slot"`
	Position      int                   `json:"position"`
	Kind          string                `json:"kind"`
	SharedData    json.RawMessage       `json:"shared_data"`
	Attachments   []hotSharedAttachment `json:"attachments"`
}

type hotSharedAttachment struct {
	ReferencePath       string    `json:"reference_path"`
	SelectorKind        string    `json:"selector_kind"`
	FileID              uuid.UUID `json:"file_id,omitempty"`
	MissingKind         string    `json:"missing_kind,omitempty"`
	AllowedMIMETypes    []string  `json:"allowed_mime_types,omitempty"`
	AllowedMIMEPrefixes []string  `json:"allowed_mime_prefixes,omitempty"`
}

type hotSharedLocale struct {
	BlockID       uuid.UUID       `json:"block_id"`
	Locale        string          `json:"locale"`
	LocalizedData json.RawMessage `json:"localized_data"`
}

type hotSharedFile struct {
	ID                uuid.UUID `json:"id"`
	MIMEType          string    `json:"mime_type"`
	DeleteRequestedAt *string   `json:"delete_requested_at"`
}

type hotSharedResultRow struct {
	Status      string          `gorm:"column:status"`
	Profile     string          `gorm:"column:profile"`
	Revision    uuid.UUID       `gorm:"column:revision"`
	Targets     json.RawMessage `gorm:"column:targets"`
	Locales     json.RawMessage `gorm:"column:locales"`
	Attachments json.RawMessage `gorm:"column:attachments"`
	Files       json.RawMessage `gorm:"column:files"`
}

func (s *Store) applySharedCTEPostgres(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
) (Result, bool, error) {
	if err := validateOperationIDs(batch); err != nil {
		return Result{}, true, err
	}
	inputs := make([]hotSharedInput, len(batch.Upserts))
	for index, block := range batch.Upserts {
		input := hotSharedInput{
			BlockID: block.ID, ParentBlockID: cloneUUIDPointer(block.ParentID),
			ContainerSlot: block.ContainerSlot, Position: block.Position,
			Kind: block.Kind, SharedData: block.SharedData,
		}
		for _, reference := range batch.validatedBaseReferences[block.ID] {
			selectorKind := "active"
			if reference.Missing {
				selectorKind = "missing"
			}
			input.Attachments = append(input.Attachments, hotSharedAttachment{
				ReferencePath: reference.ReferencePath, SelectorKind: selectorKind,
				FileID: reference.FileID, MissingKind: reference.MissingMediaKind,
				AllowedMIMETypes:    append([]string(nil), reference.AllowedMIMETypes...),
				AllowedMIMEPrefixes: append([]string(nil), reference.AllowedMIMEPrefixes...),
			})
		}
		inputs[index] = input
	}
	payload, err := json.Marshal(inputs)
	if err != nil {
		return Result{}, true, fmt.Errorf("encode shared content Block mutation: %w", err)
	}
	var row hotSharedResultRow
	err = tx.WithContext(ctx).Raw(hotSharedMutationSQL,
		string(payload), batch.DocumentID, batch.DocumentID,
		s.newUUID(), s.now(), batch.validatedProfile, batch.ExpectedRevision,
		batch.DocumentID, batch.ExpectedRevision, batch.validatedProfile,
	).Scan(&row).Error
	if err != nil {
		return Result{}, true, fmt.Errorf("apply shared content Block mutation: %w", err)
	}
	switch row.Status {
	case "not_found":
		return Result{}, true, ErrDocumentNotFound
	case "stale":
		return Result{}, true, staleRevision(row.Revision)
	case "invalid_profile":
		return Result{}, true, fmt.Errorf("%w: mutation profile does not match document", ErrInvalidMutation)
	case "cross_document":
		return Result{}, true, ErrCrossDocument
	case "missing_target", "structural_change":
		return Result{}, false, nil
	case "missing_file", "pending_file":
		return Result{}, true, fmt.Errorf("%w: shared Block File is unavailable", ErrFileReference)
	case "missing_selector":
		return Result{}, true, fmt.Errorf("%w: missing attachment is restore-only", ErrFileReference)
	case "noop", "applied":
	default:
		return Result{}, true, fmt.Errorf("%w: unexpected shared mutation status %q", ErrInvalidMutation, row.Status)
	}
	result, err := s.validateHotSharedResult(ctx, tx, row, batch, domain)
	return result, true, err
}

func (s *Store) validateHotSharedResult(
	ctx context.Context,
	tx *gorm.DB,
	row hotSharedResultRow,
	batch Batch,
	domain DomainContext,
) (Result, error) {
	var targetRows []hotReorderBlock
	if err := json.Unmarshal(row.Targets, &targetRows); err != nil {
		return Result{}, fmt.Errorf("%w: decode shared mutation targets: %v", ErrInvalidMutation, err)
	}
	var localeRows []hotSharedLocale
	if err := json.Unmarshal(row.Locales, &localeRows); err != nil {
		return Result{}, fmt.Errorf("%w: decode shared mutation locales: %v", ErrInvalidMutation, err)
	}
	var attachmentRows []hotReorderAttachment
	if err := json.Unmarshal(row.Attachments, &attachmentRows); err != nil {
		return Result{}, fmt.Errorf("%w: decode shared mutation attachments: %v", ErrInvalidMutation, err)
	}
	var fileRows []hotSharedFile
	if err := json.Unmarshal(row.Files, &fileRows); err != nil {
		return Result{}, fmt.Errorf("%w: decode shared mutation Files: %v", ErrInvalidMutation, err)
	}
	stored := make(map[uuid.UUID]FullBlock, len(targetRows))
	for _, target := range targetRows {
		block, err := baseBlockFromHotReorder(target)
		if err != nil {
			return Result{}, err
		}
		stored[block.ID] = block
	}
	locales := make(map[uuid.UUID]map[string]json.RawMessage)
	for _, locale := range localeRows {
		data, err := canonicalObject(locale.LocalizedData)
		if err != nil {
			return Result{}, fmt.Errorf("%w: stored Block %s locale %s data: %v", ErrInvalidMutation, locale.BlockID, locale.Locale, err)
		}
		if locales[locale.BlockID] == nil {
			locales[locale.BlockID] = make(map[string]json.RawMessage)
		}
		locales[locale.BlockID][locale.Locale] = data
	}
	storedAttachments := make(map[uuid.UUID][]FileReference)
	for _, value := range attachmentRows {
		reference, err := fileReferenceFromAttachmentRow(blockAttachmentRow{
			BlockID: value.BlockID, ReferencePath: value.ReferencePath,
			SelectorKind: value.SelectorKind, FileID: value.FileID, MissingKind: value.MissingKind,
		})
		if err != nil {
			return Result{}, err
		}
		storedAttachments[value.BlockID] = append(storedAttachments[value.BlockID], reference)
	}
	beforeBlocks := make(map[uuid.UUID]FullBlock, len(batch.Upserts))
	afterBlocks := make(map[uuid.UUID]FullBlock, len(batch.Upserts))
	changedIDs := make([]uuid.UUID, 0, len(batch.Upserts))
	for _, input := range batch.Upserts {
		source, exists := locales[input.ID][domain.SourceLocale]
		if !exists {
			return Result{}, fmt.Errorf("%w: Block %s has no source locale overlay", ErrInvalidMutation, input.ID)
		}
		beforeCandidate := stored[input.ID]
		beforeCandidate.LocalizedData = source
		before, err := normalizeBlock(s.contract, row.Profile, beforeCandidate)
		if err != nil {
			return Result{}, err
		}
		if !sameFileReferences(before.FileReferences, storedAttachments[input.ID]) {
			return Result{}, fmt.Errorf("%w: stored File references differ from Block %s payload", ErrInvalidMutation, input.ID)
		}
		afterCandidate := FullBlock{BaseBlock: input, LocalizedData: source}
		after, err := normalizeBlock(s.contract, row.Profile, afterCandidate)
		if err != nil {
			return Result{}, err
		}
		if !sameFileReferences(after.FileReferences, batch.validatedBaseReferences[input.ID]) {
			return Result{}, fmt.Errorf("%w: generated shared File references differ for Block %s", ErrInvalidMutation, input.ID)
		}
		for locale, localized := range locales[input.ID] {
			candidate := after
			candidate.LocalizedData = localized
			normalized, err := normalizeBlock(s.contract, row.Profile, candidate)
			if err != nil || !sameBlockShared(after, normalized) {
				return Result{}, fmt.Errorf("%w: Block %s locale %s is incompatible with shared edit", ErrInvalidMutation, input.ID, locale)
			}
		}
		beforeBlocks[input.ID] = before
		afterBlocks[input.ID] = after
		if !sameBlockShared(before, after) {
			changedIDs = append(changedIDs, input.ID)
		}
	}
	sourceChanged := false
	if len(changedIDs) > 0 {
		var err error
		sourceChanged, err = s.contract.TranslationSourceChanged(
			row.Profile, orderedAffectedBlocks(beforeBlocks, changedIDs), orderedAffectedBlocks(afterBlocks, changedIDs),
		)
		if err != nil {
			return Result{}, fmt.Errorf("%w: derive affected translation source change: %v", ErrInvalidMutation, err)
		}
	}
	files := make(map[uuid.UUID]File, len(fileRows))
	for _, value := range fileRows {
		file := File{ID: value.ID, MIMEType: value.MIMEType}
		if value.DeleteRequestedAt != nil {
			marker := s.now()
			file.DeleteRequestedAt = &marker
		}
		files[value.ID] = file
	}
	for _, blockID := range changedIDs {
		previous := referenceMap(beforeBlocks[blockID].FileReferences)
		block := afterBlocks[blockID]
		for _, reference := range block.FileReferences {
			if current, unchanged := previous[reference.ReferencePath]; unchanged &&
				!current.Missing && current.FileID == reference.FileID {
				continue
			}
			file, exists := files[reference.FileID]
			if !exists || file.DeleteRequestedAt != nil || !validateMIME(reference, file.MIMEType) {
				return Result{}, fmt.Errorf("%w: File %s is unavailable or incompatible", ErrFileReference, reference.FileID)
			}
			document := Document{ID: batch.DocumentID, Profile: row.Profile, Revision: row.Revision}
			if err := s.reuse.AuthorizeFileReuse(ctx, tx, document, block, reference, file); err != nil {
				return Result{}, fmt.Errorf("%w: authorize File %s reuse: %v", ErrFileReference, reference.FileID, err)
			}
		}
	}
	if row.Status == "noop" {
		return Result{DocumentRevision: row.Revision}, nil
	}
	return Result{
		DocumentRevision: row.Revision, Changed: true, ContentChanged: true,
		TranslationSourceChanged: sourceChanged,
	}, nil
}

func orderedAffectedBlocks(blocks map[uuid.UUID]FullBlock, ids []uuid.UUID) []FullBlock {
	selected := make(map[uuid.UUID]FullBlock, len(ids))
	for _, id := range ids {
		selected[id] = blocks[id]
	}
	return orderedBlocks(selected)
}

const hotSharedMutationSQL = `
WITH input AS MATERIALIZED (
    SELECT value.block_id, value.parent_block_id, value.container_slot,
           value.position, value.kind, value.shared_data, value.attachments
    FROM jsonb_to_recordset(?::jsonb) AS value(
        block_id uuid, parent_block_id uuid, container_slot text,
        position integer, kind text, shared_data jsonb, attachments jsonb
    )
),
input_attachments AS MATERIALIZED (
    SELECT input.block_id, attachment.reference_path, attachment.selector_kind,
           NULLIF(attachment.file_id, '00000000-0000-0000-0000-000000000000'::uuid) AS file_id,
           NULLIF(attachment.missing_kind, '') AS missing_kind
    FROM input
    CROSS JOIN LATERAL jsonb_to_recordset(COALESCE(input.attachments, '[]'::jsonb)) AS attachment(
        reference_path text, selector_kind text, file_id uuid, missing_kind text,
        allowed_mime_types jsonb, allowed_mime_prefixes jsonb
    )
),
document AS MATERIALIZED (
    SELECT id, profile, revision FROM content_document WHERE id = ?::uuid
),
targets AS MATERIALIZED (
    SELECT input.*, block.id AS owned_block_id, block.document_id,
           block.parent_block_id AS old_parent_block_id,
           block.container_slot AS old_container_slot,
           block.position AS old_position, block.kind AS old_kind,
           block.shared_data AS old_shared_data
    FROM input LEFT JOIN content_block AS block ON block.id = input.block_id
),
stored_attachments AS MATERIALIZED (
    SELECT attachment.* FROM content_block_attachment AS attachment
    JOIN targets ON targets.owned_block_id = attachment.block_id
),
locked_policy_segments AS MATERIALIZED (
    SELECT segment.id
    FROM audience_segment AS segment
    JOIN content_block_attachment_download_audience_segment AS association
      ON association.audience_segment_id = segment.id
    JOIN stored_attachments AS stored
      ON stored.block_id = association.block_id
     AND stored.reference_path = association.reference_path
    ORDER BY segment.id
    FOR UPDATE OF segment
),
locked_files AS MATERIALIZED (
    SELECT file.id, file.mime_type, file.delete_requested_at
    FROM file
    JOIN (SELECT DISTINCT file_id FROM input_attachments WHERE selector_kind = 'active') AS requested
      ON requested.file_id = file.id
    ORDER BY file.id
    FOR UPDATE OF file
),
changed_targets AS MATERIALIZED (
    SELECT targets.block_id
    FROM targets
    WHERE targets.owned_block_id IS NOT NULL
      AND (
          targets.old_shared_data IS DISTINCT FROM targets.shared_data
          OR EXISTS (
              SELECT 1 FROM input_attachments AS desired
              WHERE desired.block_id = targets.block_id
                AND NOT EXISTS (
                    SELECT 1 FROM stored_attachments AS stored
                    WHERE stored.block_id = desired.block_id
                      AND stored.reference_path = desired.reference_path
                      AND stored.selector_kind = desired.selector_kind
                      AND stored.file_id IS NOT DISTINCT FROM desired.file_id
                      AND stored.missing_kind IS NOT DISTINCT FROM desired.missing_kind
                )
          )
          OR EXISTS (
              SELECT 1 FROM stored_attachments AS stored
              WHERE stored.block_id = targets.block_id
                AND NOT EXISTS (
                    SELECT 1 FROM input_attachments AS desired
                    WHERE desired.block_id = stored.block_id
                      AND desired.reference_path = stored.reference_path
                      AND desired.selector_kind = stored.selector_kind
                      AND desired.file_id IS NOT DISTINCT FROM stored.file_id
                      AND desired.missing_kind IS NOT DISTINCT FROM stored.missing_kind
                )
          )
      )
),
assessment AS MATERIALIZED (
    SELECT count(*) FILTER (WHERE owned_block_id IS NULL) AS missing_target_count,
           count(*) FILTER (WHERE owned_block_id IS NOT NULL AND document_id <> ?::uuid) AS cross_document_count,
           count(*) FILTER (WHERE owned_block_id IS NOT NULL AND (
               old_parent_block_id IS DISTINCT FROM parent_block_id OR
               old_container_slot IS DISTINCT FROM container_slot OR
               old_position IS DISTINCT FROM position OR old_kind IS DISTINCT FROM kind
           )) AS structural_change_count,
           (SELECT count(*) FROM changed_targets) AS changed_count,
           (SELECT count(*) FROM input_attachments WHERE selector_kind = 'missing') AS missing_selector_count,
           (SELECT count(DISTINCT file_id) FROM input_attachments WHERE selector_kind = 'active') -
             (SELECT count(*) FROM locked_files) AS missing_file_count,
           (SELECT count(*) FROM locked_files WHERE delete_requested_at IS NOT NULL) AS pending_file_count,
           (SELECT count(*) FROM locked_policy_segments) AS locked_policy_segment_count
    FROM targets
),
locale_rows AS MATERIALIZED (
    SELECT locale.* FROM content_block_locale AS locale
    JOIN targets ON targets.owned_block_id = locale.block_id
),
bumped AS (
    UPDATE content_document AS root
       SET revision = ?::uuid, updated_at = ?::timestamptz
     WHERE root.id = (SELECT id FROM document)
       AND root.profile = ?::text
       AND root.revision = ?::uuid
       AND (SELECT missing_target_count FROM assessment) = 0
       AND (SELECT cross_document_count FROM assessment) = 0
       AND (SELECT structural_change_count FROM assessment) = 0
       AND (SELECT missing_selector_count FROM assessment) = 0
       AND (SELECT missing_file_count FROM assessment) = 0
       AND (SELECT pending_file_count FROM assessment) = 0
       AND (SELECT changed_count FROM assessment) > 0
    RETURNING root.id, root.profile, root.revision
),
updated AS (
    UPDATE content_block AS block
       SET shared_data = input.shared_data, updated_at = clock_timestamp()
      FROM input, bumped
     WHERE block.id = input.block_id AND block.document_id = ?::uuid
       AND EXISTS (SELECT 1 FROM changed_targets WHERE changed_targets.block_id = block.id)
    RETURNING block.id
),
deleted_attachments AS (
    DELETE FROM content_block_attachment AS stored
    USING changed_targets, bumped
    WHERE stored.block_id = changed_targets.block_id
      AND NOT EXISTS (
          SELECT 1 FROM input_attachments AS desired
          WHERE desired.block_id = stored.block_id
            AND desired.reference_path = stored.reference_path
      )
    RETURNING stored.block_id
),
reset_attachment_segments AS (
    DELETE FROM content_block_attachment_download_audience_segment AS segment
    USING input_attachments AS desired, stored_attachments AS stored, changed_targets, bumped
    WHERE segment.block_id = desired.block_id
      AND segment.reference_path = desired.reference_path
      AND stored.block_id = desired.block_id
      AND stored.reference_path = desired.reference_path
      AND changed_targets.block_id = desired.block_id
      AND (
          stored.selector_kind IS DISTINCT FROM desired.selector_kind OR
          stored.file_id IS DISTINCT FROM desired.file_id OR
          stored.missing_kind IS DISTINCT FROM desired.missing_kind
      )
    RETURNING segment.block_id
),
upserted_attachments AS (
    INSERT INTO content_block_attachment(
        block_id, reference_path, selector_kind, file_id, missing_kind, download_audience
    )
    SELECT desired.block_id, desired.reference_path, desired.selector_kind,
           desired.file_id, desired.missing_kind, 'disabled'
    FROM input_attachments AS desired
    JOIN changed_targets ON changed_targets.block_id = desired.block_id
    CROSS JOIN bumped
    ON CONFLICT (block_id, reference_path) DO UPDATE SET
        selector_kind = EXCLUDED.selector_kind,
        file_id = EXCLUDED.file_id,
        missing_kind = EXCLUDED.missing_kind,
        download_audience = CASE
            WHEN content_block_attachment.selector_kind IS NOT DISTINCT FROM EXCLUDED.selector_kind
             AND content_block_attachment.file_id IS NOT DISTINCT FROM EXCLUDED.file_id
             AND content_block_attachment.missing_kind IS NOT DISTINCT FROM EXCLUDED.missing_kind
            THEN content_block_attachment.download_audience
            ELSE 'disabled'
        END
    RETURNING block_id
)
SELECT CASE
           WHEN document.id IS NULL THEN 'not_found'
           WHEN document.revision <> ?::uuid THEN 'stale'
           WHEN document.profile <> ?::text THEN 'invalid_profile'
           WHEN assessment.cross_document_count > 0 THEN 'cross_document'
           WHEN assessment.missing_target_count > 0 THEN 'missing_target'
           WHEN assessment.structural_change_count > 0 THEN 'structural_change'
           WHEN assessment.missing_selector_count > 0 THEN 'missing_selector'
           WHEN assessment.missing_file_count > 0 THEN 'missing_file'
           WHEN assessment.pending_file_count > 0 THEN 'pending_file'
           WHEN assessment.changed_count = 0 THEN 'noop'
           WHEN bumped.id IS NOT NULL THEN 'applied'
           ELSE 'stale'
       END AS status,
       document.profile,
       COALESCE(bumped.revision, document.revision) AS revision,
       COALESCE((SELECT jsonb_agg(jsonb_build_object(
           'id', owned_block_id, 'document_id', document_id,
           'parent_block_id', old_parent_block_id, 'container_slot', old_container_slot,
           'position', old_position, 'kind', old_kind, 'shared_data', old_shared_data
       ) ORDER BY block_id) FROM targets WHERE owned_block_id IS NOT NULL), '[]'::jsonb) AS targets,
       COALESCE((SELECT jsonb_agg(to_jsonb(locale_rows) ORDER BY block_id, locale) FROM locale_rows), '[]'::jsonb) AS locales,
       COALESCE((SELECT jsonb_agg(to_jsonb(stored_attachments) ORDER BY block_id, reference_path) FROM stored_attachments), '[]'::jsonb) AS attachments,
       COALESCE((SELECT jsonb_agg(to_jsonb(locked_files) ORDER BY id) FROM locked_files), '[]'::jsonb) AS files
FROM (SELECT 1) AS singleton
LEFT JOIN document ON true
CROSS JOIN assessment
LEFT JOIN bumped ON true`
