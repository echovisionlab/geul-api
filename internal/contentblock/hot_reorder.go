package contentblock

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type hotReorderInput struct {
	BlockID       uuid.UUID  `json:"block_id"`
	ParentBlockID *uuid.UUID `json:"parent_block_id"`
	ContainerSlot string     `json:"container_slot"`
	Position      int        `json:"position"`
}

type hotReorderBlock struct {
	ID            uuid.UUID       `json:"id"`
	DocumentID    uuid.UUID       `json:"document_id"`
	ParentBlockID *uuid.UUID      `json:"parent_block_id"`
	ContainerSlot string          `json:"container_slot"`
	Position      int             `json:"position"`
	Kind          string          `json:"kind"`
	SharedData    json.RawMessage `json:"shared_data"`
}

type hotReorderLocale struct {
	BlockID       uuid.UUID       `json:"block_id"`
	LocalizedData json.RawMessage `json:"localized_data"`
}

type hotReorderAttachment struct {
	BlockID       uuid.UUID  `json:"block_id"`
	ReferencePath string     `json:"reference_path"`
	SelectorKind  string     `json:"selector_kind"`
	FileID        *uuid.UUID `json:"file_id"`
	MissingKind   *string    `json:"missing_kind"`
}

type hotReorderResultRow struct {
	Status      string          `gorm:"column:status"`
	Profile     string          `gorm:"column:profile"`
	Revision    uuid.UUID       `gorm:"column:revision"`
	Changed     int             `gorm:"column:changed_count"`
	Targets     json.RawMessage `gorm:"column:targets"`
	Siblings    json.RawMessage `gorm:"column:siblings"`
	Ancestors   json.RawMessage `gorm:"column:ancestors"`
	Locales     json.RawMessage `gorm:"column:locales"`
	Attachments json.RawMessage `gorm:"column:attachments"`
}

func (s *Store) applyReorderCTEPostgres(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
) (Result, error) {
	if err := validateOperationIDs(batch); err != nil {
		return Result{}, err
	}
	inputs := make([]hotReorderInput, len(batch.Reorders))
	for index, reorder := range batch.Reorders {
		inputs[index] = hotReorderInput{
			BlockID: reorder.BlockID, ParentBlockID: cloneUUIDPointer(reorder.ParentID),
			ContainerSlot: reorder.ContainerSlot, Position: reorder.Position,
		}
	}
	payload, err := json.Marshal(inputs)
	if err != nil {
		return Result{}, fmt.Errorf("encode content Block reorder: %w", err)
	}
	newRevision := s.newUUID()
	var row hotReorderResultRow
	err = tx.WithContext(ctx).Raw(hotReorderMutationSQL,
		string(payload), batch.DocumentID, batch.DocumentID, batch.DocumentID,
		batch.DocumentID, batch.DocumentID, domain.SourceLocale, newRevision, s.now(),
		batch.ExpectedRevision, batch.DocumentID, batch.ExpectedRevision,
	).Scan(&row).Error
	if err != nil {
		return Result{}, fmt.Errorf("apply content Block reorder: %w", err)
	}
	switch row.Status {
	case "not_found":
		return Result{}, ErrDocumentNotFound
	case "stale":
		return Result{}, staleRevision(row.Revision)
	case "cross_document":
		return Result{}, ErrCrossDocument
	case "missing_target":
		return Result{}, fmt.Errorf("%w: reorder target does not exist", ErrInvalidMutation)
	case "missing_parent":
		return Result{}, fmt.Errorf("%w: reorder parent does not exist", ErrInvalidMutation)
	case "noop", "applied":
	default:
		return Result{}, fmt.Errorf("%w: unexpected reorder status %q", ErrInvalidMutation, row.Status)
	}
	if err := s.validateHotReorderResult(row, batch, domain.SourceLocale); err != nil {
		return Result{}, err
	}
	if row.Status == "noop" {
		return Result{DocumentRevision: row.Revision}, nil
	}
	return Result{
		DocumentRevision: row.Revision, Changed: true, ContentChanged: true,
		TranslationSourceChanged: true,
	}, nil
}

func (s *Store) validateHotReorderResult(
	row hotReorderResultRow,
	batch Batch,
	sourceLocale string,
) error {
	limits, err := s.contract.Limits(row.Profile)
	if err != nil {
		return fmt.Errorf("%w: validate document profile: %v", ErrInvalidMutation, err)
	}
	var targets, siblings, ancestors []hotReorderBlock
	if err := json.Unmarshal(row.Targets, &targets); err != nil {
		return fmt.Errorf("%w: decode reorder targets: %v", ErrInvalidMutation, err)
	}
	if err := json.Unmarshal(row.Siblings, &siblings); err != nil {
		return fmt.Errorf("%w: decode reorder siblings: %v", ErrInvalidMutation, err)
	}
	if err := json.Unmarshal(row.Ancestors, &ancestors); err != nil {
		return fmt.Errorf("%w: decode reorder ancestors: %v", ErrInvalidMutation, err)
	}
	contextBlocks := make(map[uuid.UUID]FullBlock, len(targets)+len(siblings)+len(ancestors))
	requested := make(map[uuid.UUID]FullBlock, len(targets))
	for _, group := range [][]hotReorderBlock{targets, siblings, ancestors} {
		for _, value := range group {
			block, err := baseBlockFromHotReorder(value)
			if err != nil {
				return err
			}
			contextBlocks[block.ID] = block
		}
	}
	for _, value := range targets {
		block, err := baseBlockFromHotReorder(value)
		if err != nil {
			return err
		}
		requested[block.ID] = block
	}
	containers := affectedSiblingContainers(requested, batch.Reorders)
	for _, reorder := range batch.Reorders {
		block := contextBlocks[reorder.BlockID]
		block.ParentID = cloneUUIDPointer(reorder.ParentID)
		block.ContainerSlot = reorder.ContainerSlot
		block.Position = reorder.Position
		contextBlocks[reorder.BlockID] = block
	}
	validationIDs, err := affectedAncestryIDs(contextBlocks, batch.Reorders, limits.MaxDepth)
	if err != nil {
		return err
	}
	var localeRows []hotReorderLocale
	if err := json.Unmarshal(row.Locales, &localeRows); err != nil {
		return fmt.Errorf("%w: decode reorder locales: %v", ErrInvalidMutation, err)
	}
	locales := make(map[uuid.UUID]json.RawMessage, len(localeRows))
	for _, locale := range localeRows {
		data, err := canonicalObject(locale.LocalizedData)
		if err != nil {
			return fmt.Errorf("%w: stored Block %s locale %s data: %v", ErrInvalidMutation, locale.BlockID, sourceLocale, err)
		}
		locales[locale.BlockID] = data
	}
	var attachmentRows []hotReorderAttachment
	if err := json.Unmarshal(row.Attachments, &attachmentRows); err != nil {
		return fmt.Errorf("%w: decode reorder attachments: %v", ErrInvalidMutation, err)
	}
	attachments := make(map[uuid.UUID][]FileReference)
	for _, value := range attachmentRows {
		reference, err := fileReferenceFromAttachmentRow(blockAttachmentRow{
			BlockID: value.BlockID, ReferencePath: value.ReferencePath,
			SelectorKind: value.SelectorKind, FileID: value.FileID, MissingKind: value.MissingKind,
		})
		if err != nil {
			return err
		}
		attachments[value.BlockID] = append(attachments[value.BlockID], reference)
	}
	for _, blockID := range validationIDs {
		localized, exists := locales[blockID]
		if !exists {
			return fmt.Errorf("%w: Block %s has no source locale overlay", ErrInvalidMutation, blockID)
		}
		block := contextBlocks[blockID]
		block.LocalizedData = localized
		normalized, err := normalizeBlock(s.contract, row.Profile, block)
		if err != nil {
			return err
		}
		if !sameFileReferences(normalized.FileReferences, attachments[blockID]) {
			return fmt.Errorf("%w: stored File references differ from Block %s payload", ErrInvalidMutation, blockID)
		}
		contextBlocks[blockID] = normalized
	}
	if err := s.validateAffectedParents(row.Profile, contextBlocks, validationIDs); err != nil {
		return err
	}
	return validateAffectedSiblingDensity(contextBlocks, containers)
}

func baseBlockFromHotReorder(value hotReorderBlock) (FullBlock, error) {
	shared, err := canonicalObject(value.SharedData)
	if err != nil {
		return FullBlock{}, fmt.Errorf("%w: stored Block %s shared data: %v", ErrInvalidMutation, value.ID, err)
	}
	return FullBlock{BaseBlock: BaseBlock{
		ID: value.ID, ParentID: cloneUUIDPointer(value.ParentBlockID),
		ContainerSlot: value.ContainerSlot, Position: value.Position,
		Kind: value.Kind, SharedData: shared,
	}}, nil
}

const hotReorderMutationSQL = `
WITH RECURSIVE input AS MATERIALIZED (
    SELECT value.block_id, value.parent_block_id, value.container_slot, value.position
    FROM jsonb_to_recordset(?::jsonb) AS value(
        block_id uuid, parent_block_id uuid, container_slot text, position integer
    )
),
document AS MATERIALIZED (
    SELECT id, profile, revision
    FROM content_document
    WHERE id = ?::uuid
),
targets AS MATERIALIZED (
    SELECT input.*, block.id AS owned_block_id, block.document_id,
           block.parent_block_id AS old_parent_block_id,
           block.container_slot AS old_container_slot,
           block.position AS old_position,
           block.kind, block.shared_data
    FROM input
    LEFT JOIN content_block AS block ON block.id = input.block_id
),
requested_refs AS MATERIALIZED (
    SELECT input.block_id AS requested_id, true AS target, block.id, block.document_id
    FROM input LEFT JOIN content_block AS block ON block.id = input.block_id
    UNION ALL
    SELECT input.parent_block_id, false, block.id, block.document_id
    FROM input LEFT JOIN content_block AS block ON block.id = input.parent_block_id
    WHERE input.parent_block_id IS NOT NULL
),
assessment AS MATERIALIZED (
    SELECT (SELECT count(*) FROM requested_refs WHERE target AND id IS NULL) AS missing_target_count,
           (SELECT count(*) FROM requested_refs WHERE NOT target AND id IS NULL) AS missing_parent_count,
           (SELECT count(*) FROM requested_refs WHERE id IS NOT NULL AND document_id <> ?::uuid) AS cross_document_count,
           (SELECT count(*) FROM targets WHERE owned_block_id IS NOT NULL AND (
               old_parent_block_id IS DISTINCT FROM parent_block_id OR
               old_container_slot IS DISTINCT FROM container_slot OR
               old_position IS DISTINCT FROM position
           )) AS changed_count
),
containers AS MATERIALIZED (
    SELECT DISTINCT targets.old_parent_block_id AS parent_block_id, targets.old_container_slot AS container_slot
    FROM targets WHERE targets.document_id = ?::uuid
    UNION
    SELECT DISTINCT input.parent_block_id, input.container_slot FROM input
),
siblings AS MATERIALIZED (
    SELECT block.*
    FROM content_block AS block
    JOIN containers ON block.parent_block_id IS NOT DISTINCT FROM containers.parent_block_id
                   AND block.container_slot = containers.container_slot
    WHERE block.document_id = ?::uuid
	  AND NOT EXISTS (SELECT 1 FROM input WHERE input.block_id = block.id)
),
ancestry AS MATERIALIZED (
    SELECT block.*, ARRAY[block.id]::uuid[] AS visited
    FROM content_block AS block
    JOIN (SELECT DISTINCT parent_block_id FROM input WHERE parent_block_id IS NOT NULL) AS seed
      ON seed.parent_block_id = block.id
    WHERE block.document_id = ?::uuid

    UNION ALL

    SELECT parent.*, ancestry.visited || parent.id
    FROM ancestry
    LEFT JOIN input AS moved ON moved.block_id = ancestry.id
    JOIN content_block AS parent
      ON parent.document_id = ancestry.document_id
     AND parent.id = CASE WHEN moved.block_id IS NOT NULL THEN moved.parent_block_id ELSE ancestry.parent_block_id END
    WHERE parent.id <> ALL (ancestry.visited)
),
validation_ids AS MATERIALIZED (
    SELECT owned_block_id AS id FROM targets WHERE owned_block_id IS NOT NULL
    UNION SELECT id FROM ancestry
),
locale_rows AS MATERIALIZED (
    SELECT locale_row.block_id, locale_row.localized_data
    FROM content_block_locale AS locale_row
    JOIN validation_ids ON validation_ids.id = locale_row.block_id
    WHERE locale_row.locale = ?::text
),
attachment_rows AS MATERIALIZED (
    SELECT attachment.*
    FROM content_block_attachment AS attachment
    JOIN validation_ids ON validation_ids.id = attachment.block_id
),
bumped AS (
    UPDATE content_document AS root
       SET revision = ?::uuid, updated_at = ?::timestamptz
     WHERE root.id = (SELECT id FROM document)
       AND root.revision = ?::uuid
       AND (SELECT missing_target_count FROM assessment) = 0
       AND (SELECT missing_parent_count FROM assessment) = 0
       AND (SELECT cross_document_count FROM assessment) = 0
       AND (SELECT changed_count FROM assessment) > 0
    RETURNING root.id, root.profile, root.revision
),
updated AS (
    UPDATE content_block AS block
       SET parent_block_id = input.parent_block_id,
           container_slot = input.container_slot,
           position = input.position,
           updated_at = clock_timestamp()
      FROM input, bumped
     WHERE block.id = input.block_id
       AND block.document_id = ?::uuid
       AND (block.parent_block_id, block.container_slot, block.position)
           IS DISTINCT FROM (input.parent_block_id, input.container_slot, input.position)
    RETURNING block.id
)
SELECT CASE
           WHEN document.id IS NULL THEN 'not_found'
           WHEN document.revision <> ?::uuid THEN 'stale'
           WHEN assessment.cross_document_count > 0 THEN 'cross_document'
           WHEN assessment.missing_target_count > 0 THEN 'missing_target'
           WHEN assessment.missing_parent_count > 0 THEN 'missing_parent'
           WHEN assessment.changed_count = 0 THEN 'noop'
           WHEN bumped.id IS NOT NULL THEN 'applied'
           ELSE 'stale'
       END AS status,
       document.profile,
       COALESCE(bumped.revision, document.revision) AS revision,
       assessment.changed_count,
       COALESCE((SELECT jsonb_agg(jsonb_build_object(
           'id', owned_block_id, 'document_id', document_id,
           'parent_block_id', old_parent_block_id, 'container_slot', old_container_slot,
           'position', old_position, 'kind', kind, 'shared_data', shared_data
       ) ORDER BY block_id) FROM targets WHERE owned_block_id IS NOT NULL), '[]'::jsonb) AS targets,
       COALESCE((SELECT jsonb_agg(to_jsonb(siblings) ORDER BY id) FROM siblings), '[]'::jsonb) AS siblings,
       COALESCE((SELECT jsonb_agg(to_jsonb(ancestry) - 'visited' ORDER BY id)
                 FROM ancestry WHERE NOT EXISTS (SELECT 1 FROM input WHERE input.block_id = ancestry.id)), '[]'::jsonb) AS ancestors,
       COALESCE((SELECT jsonb_agg(to_jsonb(locale_rows) ORDER BY block_id) FROM locale_rows), '[]'::jsonb) AS locales,
       COALESCE((SELECT jsonb_agg(to_jsonb(attachment_rows) ORDER BY block_id, reference_path) FROM attachment_rows), '[]'::jsonb) AS attachments
FROM (SELECT 1) AS singleton
LEFT JOIN document ON true
CROSS JOIN assessment
LEFT JOIN bumped ON true`
