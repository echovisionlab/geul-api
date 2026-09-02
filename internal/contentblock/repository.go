package contentblock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repository struct{}

const persistenceChunkRows = 1000

func (repository) createDocument(ctx context.Context, tx *gorm.DB, row documentRow) error {
	if err := tx.WithContext(ctx).Create(&row).Error; err != nil {
		return fmt.Errorf("create content document: %w", err)
	}
	return nil
}

func (repository) loadDocument(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	lockStrength string,
) (Document, error) {
	var row documentRow
	query := tx.WithContext(ctx)
	if lockStrength != "" {
		query = query.Clauses(clause.Locking{Strength: lockStrength})
	}
	if err := query.Where("id = ?", documentID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Document{}, ErrDocumentNotFound
		}
		return Document{}, fmt.Errorf("lock content document: %w", err)
	}
	return documentFromRow(row), nil
}

func (repository) loadAggregate(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	lockStrength string,
) (aggregate, error) {
	var document documentRow
	documentQuery := tx.WithContext(ctx)
	if lockStrength != "" {
		documentQuery = documentQuery.Clauses(clause.Locking{Strength: lockStrength})
	}
	if err := documentQuery.
		Where("id = ?", documentID).
		Take(&document).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return aggregate{}, ErrDocumentNotFound
		}
		return aggregate{}, fmt.Errorf("lock content document: %w", err)
	}

	result := newAggregate(documentFromRow(document))
	var blocks []blockRow
	if err := tx.WithContext(ctx).
		Where("document_id = ?", documentID).
		Find(&blocks).Error; err != nil {
		return aggregate{}, fmt.Errorf("load content blocks: %w", err)
	}
	for _, block := range blocks {
		sharedData, err := canonicalObject(block.SharedData)
		if err != nil {
			return aggregate{}, fmt.Errorf(
				"%w: stored Block %s shared data: %v",
				ErrInvalidMutation,
				block.ID,
				err,
			)
		}
		result.blocks[block.ID] = FullBlock{
			BaseBlock: BaseBlock{
				ID:            block.ID,
				ParentID:      block.ParentBlockID,
				ContainerSlot: block.ContainerSlot,
				Position:      block.Position,
				Kind:          block.Kind,
				SharedData:    sharedData,
			},
		}
	}

	if len(blocks) == 0 {
		return result, nil
	}
	blockIDs := make([]uuid.UUID, 0, len(blocks))
	for _, block := range blocks {
		blockIDs = append(blockIDs, block.ID)
	}
	var locales []blockLocaleRow
	if err := tx.WithContext(ctx).
		Where("block_id IN ?", blockIDs).
		Find(&locales).Error; err != nil {
		return aggregate{}, fmt.Errorf("load content block locales: %w", err)
	}
	for _, locale := range locales {
		localizedData, err := canonicalObject(locale.LocalizedData)
		if err != nil {
			return aggregate{}, fmt.Errorf(
				"%w: stored Block %s locale %s data: %v",
				ErrInvalidMutation,
				locale.BlockID,
				locale.Locale,
				err,
			)
		}
		if result.locales[locale.BlockID] == nil {
			result.locales[locale.BlockID] = make(map[string]json.RawMessage)
		}
		result.locales[locale.BlockID][locale.Locale] = localizedData
	}

	var files []blockAttachmentRow
	if err := tx.WithContext(ctx).
		Where("block_id IN ?", blockIDs).
		Find(&files).Error; err != nil {
		return aggregate{}, fmt.Errorf("load content Block attachments: %w", err)
	}
	for _, attachment := range files {
		reference, err := fileReferenceFromAttachmentRow(attachment)
		if err != nil {
			return aggregate{}, err
		}
		block := result.blocks[attachment.BlockID]
		block.FileReferences = append(block.FileReferences, FileReference{
			BlockID:          reference.BlockID,
			ReferencePath:    reference.ReferencePath,
			FileID:           reference.FileID,
			Missing:          reference.Missing,
			MissingMediaKind: reference.MissingMediaKind,
		})
		result.blocks[attachment.BlockID] = block
	}
	return result, nil
}

func (repository) blockDocuments(
	ctx context.Context,
	tx *gorm.DB,
	blockIDs []uuid.UUID,
) (map[uuid.UUID]uuid.UUID, error) {
	if len(blockIDs) == 0 {
		return map[uuid.UUID]uuid.UUID{}, nil
	}
	blockIDs = uniqueUUIDs(blockIDs)
	result := make(map[uuid.UUID]uuid.UUID, len(blockIDs))
	for start := 0; start < len(blockIDs); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(blockIDs))
		var rows []struct {
			ID         uuid.UUID `gorm:"column:id"`
			DocumentID uuid.UUID `gorm:"column:document_id"`
		}
		if err := tx.WithContext(ctx).
			Table("content_block").
			Select("id", "document_id").
			Where("id IN ?", blockIDs[start:end]).
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("resolve content block documents: %w", err)
		}
		for _, row := range rows {
			result[row.ID] = row.DocumentID
		}
	}
	return result, nil
}

func (repository) loadPublicationAttachments(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]PublicationAttachment, error) {
	var rows []blockAttachmentRow
	if err := db.WithContext(ctx).
		Table("content_block_attachment AS attachment").
		Select("attachment.block_id, attachment.reference_path, attachment.selector_kind, attachment.file_id, attachment.missing_kind").
		Joins("JOIN content_block AS block ON block.id = attachment.block_id").
		Where("block.document_id = ?", documentID).
		Order("attachment.block_id, attachment.reference_path").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load publication content Block attachments: %w", err)
	}
	attachments := make([]PublicationAttachment, 0, len(rows))
	for _, row := range rows {
		reference, err := fileReferenceFromAttachmentRow(row)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, PublicationAttachment{
			BlockID:          reference.BlockID,
			ReferencePath:    reference.ReferencePath,
			FileID:           reference.FileID,
			MissingMediaKind: reference.MissingMediaKind,
		})
	}
	return attachments, nil
}

func (repository) lockFiles(
	ctx context.Context,
	tx *gorm.DB,
	fileIDs []uuid.UUID,
) (map[uuid.UUID]File, error) {
	fileIDs = uniqueUUIDs(fileIDs)
	sort.Slice(fileIDs, func(i, j int) bool { return fileIDs[i].String() < fileIDs[j].String() })
	if len(fileIDs) == 0 {
		return map[uuid.UUID]File{}, nil
	}
	result := make(map[uuid.UUID]File, len(fileIDs))
	for start := 0; start < len(fileIDs); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(fileIDs))
		var rows []fileRow
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ?", fileIDs[start:end]).
			Order("id").
			Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("lock content block files: %w", err)
		}
		for _, row := range rows {
			result[row.ID] = File(row)
		}
	}
	return result, nil
}

func (repository) persist(
	ctx context.Context,
	tx *gorm.DB,
	before, after aggregate,
	mutation persistedMutation,
) error {
	if err := deleteBlocks(ctx, tx, before, mutation.deleteOrder); err != nil {
		return err
	}
	if err := persistBaseBlocks(ctx, tx, before, after, mutation.upsertOrder); err != nil {
		return err
	}
	if err := clearChangedKindLocales(ctx, tx, mutation); err != nil {
		return err
	}
	if err := replaceBlockAttachments(ctx, tx, after, mutation.upsertOrder); err != nil {
		return err
	}
	if err := persistLocaleMutations(ctx, tx, after, mutation.localeMutations); err != nil {
		return err
	}

	if err := persistReorders(ctx, tx, after.document.ID, mutation.reorders); err != nil {
		return err
	}
	if err := verifyPersistedMutation(ctx, tx, before, after, mutation); err != nil {
		return err
	}

	return repository{}.updateDocument(ctx, tx, before.document, after.document)
}

func (repository) persistTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	after aggregate,
	mutation persistedMutation,
) error {
	if len(mutation.upsertOrder) != 0 || len(mutation.deleteOrder) != 0 ||
		len(mutation.reorders) != 0 || len(mutation.kindChanged) != 0 {
		return fmt.Errorf("%w: target locale persistence received a shared mutation", ErrInvalidMutation)
	}
	if err := persistLocaleMutations(ctx, tx, after, mutation.localeMutations); err != nil {
		return err
	}
	return verifyPersistedMutationLocales(ctx, tx, after, mutation)
}

func verifyPersistedMutation(
	ctx context.Context,
	tx *gorm.DB,
	before, after aggregate,
	mutation persistedMutation,
) error {
	blockIDs := make([]uuid.UUID, 0, len(mutation.upsertOrder)+len(mutation.deleteOrder)+len(mutation.reorders))
	blockIDs = append(blockIDs, mutation.upsertOrder...)
	blockIDs = append(blockIDs, mutation.deleteOrder...)
	for _, reorder := range mutation.reorders {
		blockIDs = append(blockIDs, reorder.BlockID)
	}
	blockIDs = uniqueUUIDs(blockIDs)
	if len(blockIDs) > 0 {
		var rows []blockRow
		if err := tx.WithContext(ctx).
			Where("document_id = ? AND id IN ?", after.document.ID, blockIDs).
			Find(&rows).Error; err != nil {
			return fmt.Errorf("verify persisted content Blocks: %w", err)
		}
		actual := make(map[uuid.UUID]FullBlock, len(rows))
		for _, row := range rows {
			sharedData, err := canonicalObject(row.SharedData)
			if err != nil {
				return fmt.Errorf("%w: persisted Block %s shared data: %v", ErrInvalidMutation, row.ID, err)
			}
			actual[row.ID] = FullBlock{BaseBlock: BaseBlock{
				ID:            row.ID,
				ParentID:      row.ParentBlockID,
				ContainerSlot: row.ContainerSlot,
				Position:      row.Position,
				Kind:          row.Kind,
				SharedData:    sharedData,
			}}
		}
		for _, blockID := range blockIDs {
			expected, shouldExist := after.blocks[blockID]
			persisted, exists := actual[blockID]
			if exists != shouldExist || (exists && !samePersistedBlockFields(expected, persisted)) {
				return fmt.Errorf("%w: persisted affected Block %s differs", ErrInvalidMutation, blockID)
			}
		}
	}

	if err := verifyPersistedMutationLocales(ctx, tx, after, mutation); err != nil {
		return err
	}
	return verifyPersistedMutationAttachments(ctx, tx, before, after, mutation.upsertOrder)
}

func verifyPersistedMutationLocales(
	ctx context.Context,
	tx *gorm.DB,
	after aggregate,
	mutation persistedMutation,
) error {
	kindChanged := make([]uuid.UUID, 0, len(mutation.kindChanged))
	kindChangedSet := make(map[uuid.UUID]struct{}, len(mutation.kindChanged))
	for blockID, changed := range mutation.kindChanged {
		if changed {
			kindChanged = append(kindChanged, blockID)
			kindChangedSet[blockID] = struct{}{}
		}
	}
	conditions := make([]string, 0, len(mutation.localeMutations)+1)
	arguments := make([]any, 0, len(mutation.localeMutations)*2+1)
	if len(kindChanged) > 0 {
		conditions = append(conditions, "block_id IN ?")
		arguments = append(arguments, kindChanged)
	}
	requested := make(map[localeMutationKey]struct{}, len(mutation.localeMutations))
	for _, changed := range mutation.localeMutations {
		requested[localeMutationKey{blockID: changed.blockID, locale: changed.locale}] = struct{}{}
		if _, covered := kindChangedSet[changed.blockID]; covered {
			continue
		}
		conditions = append(conditions, "(block_id = ? AND locale = ?)")
		arguments = append(arguments, changed.blockID, changed.locale)
	}
	if len(conditions) == 0 {
		return nil
	}
	var rows []blockLocaleRow
	if err := tx.WithContext(ctx).
		Where(strings.Join(conditions, " OR "), arguments...).
		Find(&rows).Error; err != nil {
		return fmt.Errorf("verify persisted content Block locales: %w", err)
	}
	actual := make(map[uuid.UUID]map[string]json.RawMessage)
	for _, row := range rows {
		data, err := canonicalObject(row.LocalizedData)
		if err != nil {
			return fmt.Errorf(
				"%w: persisted Block %s locale %s data: %v",
				ErrInvalidMutation,
				row.BlockID,
				row.Locale,
				err,
			)
		}
		if actual[row.BlockID] == nil {
			actual[row.BlockID] = make(map[string]json.RawMessage)
		}
		actual[row.BlockID][row.Locale] = data
	}
	for blockID := range kindChangedSet {
		expectedLocales := after.locales[blockID]
		actualLocales := actual[blockID]
		if len(expectedLocales) != len(actualLocales) {
			return fmt.Errorf("%w: persisted affected Block %s locale set differs", ErrInvalidMutation, blockID)
		}
		for locale, expected := range expectedLocales {
			persisted, exists := actualLocales[locale]
			if !exists || !sameCanonicalJSON(expected, persisted) {
				return fmt.Errorf("%w: persisted affected Block %s locale %s differs", ErrInvalidMutation, blockID, locale)
			}
		}
	}
	for key := range requested {
		if _, covered := kindChangedSet[key.blockID]; covered {
			continue
		}
		expected, shouldExist := after.locales[key.blockID][key.locale]
		persisted, exists := actual[key.blockID][key.locale]
		if exists != shouldExist || (exists && !sameCanonicalJSON(expected, persisted)) {
			return fmt.Errorf("%w: persisted affected Block %s locale %s differs", ErrInvalidMutation, key.blockID, key.locale)
		}
	}
	return nil
}

type localeMutationKey struct {
	blockID uuid.UUID
	locale  string
}

func verifyPersistedMutationAttachments(
	ctx context.Context,
	tx *gorm.DB,
	before, after aggregate,
	upsertIDs []uuid.UUID,
) error {
	queryIDs := make([]uuid.UUID, 0, len(upsertIDs))
	for _, blockID := range upsertIDs {
		if len(before.blocks[blockID].FileReferences) > 0 ||
			len(after.blocks[blockID].FileReferences) > 0 {
			queryIDs = append(queryIDs, blockID)
		}
	}
	if len(queryIDs) == 0 {
		return nil
	}
	var rows []blockAttachmentRow
	if err := tx.WithContext(ctx).Where("block_id IN ?", queryIDs).Find(&rows).Error; err != nil {
		return fmt.Errorf("verify persisted content Block attachments: %w", err)
	}
	actual := make(map[uuid.UUID][]FileReference, len(queryIDs))
	for _, row := range rows {
		reference, err := fileReferenceFromAttachmentRow(row)
		if err != nil {
			return err
		}
		actual[row.BlockID] = append(actual[row.BlockID], reference)
	}
	for _, blockID := range queryIDs {
		if !sameFileReferences(after.blocks[blockID].FileReferences, actual[blockID]) {
			return fmt.Errorf("%w: persisted affected Block %s attachments differ", ErrInvalidMutation, blockID)
		}
	}
	return nil
}

func deleteBlocks(
	ctx context.Context,
	tx *gorm.DB,
	before aggregate,
	deleteOrder []uuid.UUID,
) error {
	blockIDs := make([]uuid.UUID, 0, len(deleteOrder))
	for _, blockID := range deleteOrder {
		if _, exists := before.blocks[blockID]; exists {
			blockIDs = append(blockIDs, blockID)
		}
	}
	for start := 0; start < len(blockIDs); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(blockIDs))
		chunk := blockIDs[start:end]
		result := tx.WithContext(ctx).
			Where("document_id = ? AND id IN ?", before.document.ID, chunk).
			Delete(&blockRow{})
		if result.Error != nil {
			return fmt.Errorf("delete content blocks: %w", result.Error)
		}
		if result.RowsAffected != int64(len(chunk)) {
			return fmt.Errorf("%w: deleted %d of %d Blocks", ErrInvalidMutation, result.RowsAffected, len(chunk))
		}
	}
	return nil
}

func persistBaseBlocks(
	ctx context.Context,
	tx *gorm.DB,
	before, after aggregate,
	upsertOrder []uuid.UUID,
) error {
	created := make([]blockRow, 0, len(upsertOrder))
	updated := make([]blockRow, 0, len(upsertOrder))
	for _, blockID := range upsertOrder {
		block := after.blocks[blockID]
		row := blockRow{
			ID: block.ID, DocumentID: after.document.ID, ParentBlockID: block.ParentID,
			ContainerSlot: block.ContainerSlot, Position: block.Position, Kind: block.Kind,
			SharedData: block.SharedData, CreatedAt: after.document.UpdatedAt, UpdatedAt: after.document.UpdatedAt,
		}
		if _, exists := before.blocks[blockID]; exists {
			updated = append(updated, row)
		} else {
			created = append(created, row)
		}
	}
	if len(created) > 0 {
		for start := 0; start < len(created); start += persistenceChunkRows {
			end := min(start+persistenceChunkRows, len(created))
			chunk := created[start:end]
			result := tx.WithContext(ctx).Create(&chunk)
			if result.Error != nil {
				return fmt.Errorf("create content blocks: %w", result.Error)
			}
			if result.RowsAffected != int64(end-start) {
				return fmt.Errorf("%w: created %d of %d Blocks", ErrInvalidMutation, result.RowsAffected, end-start)
			}
		}
	}
	if tx.Dialector.Name() != "postgres" {
		for _, row := range updated {
			result := tx.WithContext(ctx).Model(&blockRow{}).
				Where("id = ? AND document_id = ?", row.ID, row.DocumentID).
				Updates(map[string]any{
					"parent_block_id": row.ParentBlockID, "container_slot": row.ContainerSlot,
					"position": row.Position, "kind": row.Kind, "shared_data": row.SharedData,
					"updated_at": row.UpdatedAt,
				})
			if result.Error != nil {
				return fmt.Errorf("update content Block %s: %w", row.ID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: Block %s disappeared", ErrInvalidMutation, row.ID)
			}
		}
		return nil
	}
	for start := 0; start < len(updated); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(updated))
		chunk := updated[start:end]
		values := make([]string, 0, len(chunk))
		arguments := make([]any, 0, len(chunk)*7+1)
		for _, row := range chunk {
			values = append(values, "(?::uuid, ?::uuid, ?::text, ?::integer, ?::text, ?::jsonb, ?::timestamptz)")
			arguments = append(arguments, row.ID, row.ParentBlockID, row.ContainerSlot, row.Position, row.Kind, row.SharedData, row.UpdatedAt)
		}
		arguments = append(arguments, after.document.ID)
		query := `
UPDATE content_block AS block
SET parent_block_id = changed.parent_block_id,
    container_slot = changed.container_slot,
    position = changed.position,
    kind = changed.kind,
    shared_data = changed.shared_data,
    updated_at = changed.updated_at
FROM (VALUES ` + strings.Join(values, ",") + `)
    AS changed(id, parent_block_id, container_slot, position, kind, shared_data, updated_at)
WHERE block.id = changed.id
  AND block.document_id = ?::uuid`
		result := tx.WithContext(ctx).Exec(query, arguments...)
		if result.Error != nil {
			return fmt.Errorf("bulk update content Blocks: %w", result.Error)
		}
		if result.RowsAffected != int64(len(chunk)) {
			return fmt.Errorf("%w: updated %d of %d Blocks", ErrInvalidMutation, result.RowsAffected, len(chunk))
		}
	}
	return nil
}

func clearChangedKindLocales(ctx context.Context, tx *gorm.DB, mutation persistedMutation) error {
	blockIDs := make([]uuid.UUID, 0, len(mutation.kindChanged))
	for blockID, changed := range mutation.kindChanged {
		if changed {
			blockIDs = append(blockIDs, blockID)
		}
	}
	sort.Slice(blockIDs, func(i, j int) bool { return blockIDs[i].String() < blockIDs[j].String() })
	for start := 0; start < len(blockIDs); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(blockIDs))
		if err := tx.WithContext(ctx).
			Where("block_id IN ? AND locale <> ?", blockIDs[start:end], mutation.sourceLocale).
			Delete(&blockLocaleRow{}).Error; err != nil {
			return fmt.Errorf("clear incompatible Block locales: %w", err)
		}
	}
	return nil
}

func replaceBlockAttachments(
	ctx context.Context,
	tx *gorm.DB,
	after aggregate,
	upsertOrder []uuid.UUID,
) error {
	if tx.Dialector.Name() == "postgres" && len(upsertOrder) > 0 {
		var locked []struct {
			ID string `gorm:"column:id"`
		}
		if err := tx.WithContext(ctx).
			Table("audience_segment AS segment").
			Select("segment.id").
			Joins("JOIN content_block_attachment_download_audience_segment AS association ON association.audience_segment_id = segment.id").
			Where("association.block_id IN ?", upsertOrder).
			Order("segment.id").
			Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "segment"}}).
			Find(&locked).Error; err != nil {
			return fmt.Errorf("lock content Block attachment audience segments: %w", err)
		}
	}
	desired := make(map[uuid.UUID]map[string]blockAttachmentRow, len(upsertOrder))
	for _, blockID := range upsertOrder {
		desired[blockID] = make(map[string]blockAttachmentRow)
		for _, reference := range after.blocks[blockID].FileReferences {
			row := blockAttachmentRow{
				BlockID: blockID, ReferencePath: reference.ReferencePath,
				DownloadAudience: "disabled",
			}
			if reference.Missing {
				missingKind := reference.MissingMediaKind
				row.SelectorKind = "missing"
				row.MissingKind = &missingKind
			} else {
				fileID := reference.FileID
				row.SelectorKind = "active"
				row.FileID = &fileID
			}
			desired[blockID][row.ReferencePath] = row
		}
	}

	for start := 0; start < len(upsertOrder); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(upsertOrder))
		var stored []blockAttachmentRow
		if err := tx.WithContext(ctx).
			Where("block_id IN ?", upsertOrder[start:end]).
			Find(&stored).Error; err != nil {
			return fmt.Errorf("load content Block attachments: %w", err)
		}
		for _, current := range stored {
			next, exists := desired[current.BlockID][current.ReferencePath]
			if !exists {
				if err := tx.WithContext(ctx).
					Where("block_id = ? AND reference_path = ?", current.BlockID, current.ReferencePath).
					Delete(&blockAttachmentRow{}).Error; err != nil {
					return fmt.Errorf("remove content Block attachment: %w", err)
				}
				continue
			}
			delete(desired[current.BlockID], current.ReferencePath)
			if sameBlockAttachmentSelector(current, next) {
				continue
			}
			if err := tx.WithContext(ctx).
				Where("block_id = ? AND reference_path = ?", current.BlockID, current.ReferencePath).
				Delete(&blockAttachmentDownloadAudienceSegmentRow{}).Error; err != nil {
				return fmt.Errorf("reset content Block attachment audience segments: %w", err)
			}
			result := tx.WithContext(ctx).Model(&blockAttachmentRow{}).
				Where("block_id = ? AND reference_path = ?", current.BlockID, current.ReferencePath).
				Updates(map[string]any{
					"selector_kind":     next.SelectorKind,
					"file_id":           next.FileID,
					"missing_kind":      next.MissingKind,
					"download_audience": "disabled",
				})
			if result.Error != nil {
				return fmt.Errorf("replace content Block attachment: %w", result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: replaced %d of 1 content Block attachments", ErrInvalidMutation, result.RowsAffected)
			}
		}
	}
	rows := make([]blockAttachmentRow, 0)
	for _, blockID := range upsertOrder {
		paths := make([]string, 0, len(desired[blockID]))
		for path := range desired[blockID] {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			row := desired[blockID][path]
			rows = append(rows, row)
		}
	}
	if len(rows) > 0 {
		for start := 0; start < len(rows); start += persistenceChunkRows {
			end := min(start+persistenceChunkRows, len(rows))
			chunk := rows[start:end]
			result := tx.WithContext(ctx).Create(&chunk)
			if result.Error != nil {
				return fmt.Errorf("attach content Block selectors: %w", result.Error)
			}
			if result.RowsAffected != int64(end-start) {
				return fmt.Errorf("%w: attached %d of %d Block selectors", ErrInvalidMutation, result.RowsAffected, end-start)
			}
		}
	}
	return nil
}

func sameBlockAttachmentSelector(left, right blockAttachmentRow) bool {
	if left.SelectorKind != right.SelectorKind {
		return false
	}
	if (left.FileID == nil) != (right.FileID == nil) ||
		(left.MissingKind == nil) != (right.MissingKind == nil) {
		return false
	}
	if left.FileID != nil && *left.FileID != *right.FileID {
		return false
	}
	return left.MissingKind == nil || *left.MissingKind == *right.MissingKind
}

func fileReferenceFromAttachmentRow(row blockAttachmentRow) (FileReference, error) {
	reference := FileReference{BlockID: row.BlockID, ReferencePath: row.ReferencePath}
	switch row.SelectorKind {
	case "active":
		if row.FileID == nil || row.MissingKind != nil {
			return FileReference{}, fmt.Errorf("%w: malformed active Block attachment %s", ErrInvalidMutation, row.ReferencePath)
		}
		reference.FileID = *row.FileID
	case "missing":
		if row.FileID != nil || row.MissingKind == nil || *row.MissingKind == "" {
			return FileReference{}, fmt.Errorf("%w: malformed missing Block attachment %s", ErrInvalidMutation, row.ReferencePath)
		}
		switch *row.MissingKind {
		case "image", "audio", "video", "file":
		default:
			return FileReference{}, fmt.Errorf("%w: malformed missing Block attachment media kind %q", ErrInvalidMutation, *row.MissingKind)
		}
		reference.Missing = true
		reference.MissingMediaKind = *row.MissingKind
	default:
		return FileReference{}, fmt.Errorf("%w: unknown Block attachment selector %q", ErrInvalidMutation, row.SelectorKind)
	}
	return reference, nil
}

func persistLocaleMutations(
	ctx context.Context,
	tx *gorm.DB,
	after aggregate,
	mutations []localeMutation,
) error {
	deletes := make([]localeMutation, 0, len(mutations))
	upserts := make([]blockLocaleRow, 0, len(mutations))
	for _, mutation := range mutations {
		if mutation.delete {
			deletes = append(deletes, mutation)
			continue
		}
		upserts = append(upserts, blockLocaleRow{
			BlockID: mutation.blockID, Locale: mutation.locale,
			LocalizedData: localizedData(after.locales, mutation.blockID, mutation.locale),
		})
	}
	for start := 0; start < len(deletes); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(deletes))
		chunk := deletes[start:end]
		conditions := make([]string, 0, len(chunk))
		arguments := make([]any, 0, len(chunk)*2)
		for _, mutation := range chunk {
			conditions = append(conditions, "(block_id = ? AND locale = ?)")
			arguments = append(arguments, mutation.blockID, mutation.locale)
		}
		if err := tx.WithContext(ctx).
			Where(strings.Join(conditions, " OR "), arguments...).
			Delete(&blockLocaleRow{}).Error; err != nil {
			return fmt.Errorf("delete content Block locales: %w", err)
		}
	}
	if len(upserts) > 0 {
		for start := 0; start < len(upserts); start += persistenceChunkRows {
			end := min(start+persistenceChunkRows, len(upserts))
			chunk := upserts[start:end]
			result := tx.WithContext(ctx).Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "block_id"}, {Name: "locale"}},
				DoUpdates: clause.AssignmentColumns([]string{"localized_data"}),
			}).Create(&chunk)
			if result.Error != nil {
				return fmt.Errorf("upsert content Block locales: %w", result.Error)
			}
			if result.RowsAffected != int64(end-start) {
				return fmt.Errorf("%w: upserted %d of %d Block locales", ErrInvalidMutation, result.RowsAffected, end-start)
			}
		}
	}
	return nil
}

func persistReorders(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	reorders []Reorder,
) error {
	if len(reorders) == 0 {
		return nil
	}
	if tx.Dialector.Name() != "postgres" {
		for _, reorder := range reorders {
			result := tx.WithContext(ctx).
				Model(&blockRow{}).
				Where("id = ? AND document_id = ?", reorder.BlockID, documentID).
				Updates(map[string]any{
					"parent_block_id": reorder.ParentID,
					"container_slot":  reorder.ContainerSlot,
					"position":        reorder.Position,
				})
			if result.Error != nil {
				return fmt.Errorf("reorder content block %s: %w", reorder.BlockID, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: reorder target %s disappeared", ErrInvalidMutation, reorder.BlockID)
			}
		}
		return nil
	}

	for start := 0; start < len(reorders); start += persistenceChunkRows {
		end := min(start+persistenceChunkRows, len(reorders))
		chunk := reorders[start:end]
		values := make([]string, 0, len(chunk))
		arguments := make([]any, 0, len(chunk)*4+1)
		for _, reorder := range chunk {
			values = append(values, "(?::uuid, ?::uuid, ?::text, ?::integer)")
			arguments = append(arguments, reorder.BlockID, reorder.ParentID, reorder.ContainerSlot, reorder.Position)
		}
		arguments = append(arguments, documentID)
		query := `
UPDATE content_block AS block
SET parent_block_id = moved.parent_block_id,
    container_slot = moved.container_slot,
    position = moved.position,
    updated_at = now()
FROM (VALUES ` + strings.Join(values, ",") + `)
    AS moved(id, parent_block_id, container_slot, position)
WHERE block.id = moved.id
  AND block.document_id = ?::uuid`
		result := tx.WithContext(ctx).Exec(query, arguments...)
		if result.Error != nil {
			return fmt.Errorf("bulk reorder content blocks: %w", result.Error)
		}
		if result.RowsAffected != int64(len(chunk)) {
			return fmt.Errorf(
				"%w: reordered %d of %d Blocks",
				ErrInvalidMutation,
				result.RowsAffected,
				len(chunk),
			)
		}
	}
	return nil
}

func samePersistedBlockFields(expected, actual FullBlock) bool {
	return expected.ID == actual.ID &&
		equalUUIDPointers(expected.ParentID, actual.ParentID) &&
		expected.ContainerSlot == actual.ContainerSlot &&
		expected.Position == actual.Position &&
		expected.Kind == actual.Kind &&
		sameCanonicalJSON(expected.SharedData, actual.SharedData)
}

func (repository) updateDocument(
	ctx context.Context,
	tx *gorm.DB,
	before, after Document,
) error {
	result := tx.WithContext(ctx).
		Model(&documentRow{}).
		Where("id = ? AND revision = ?", after.ID, before.Revision).
		Updates(map[string]any{
			"revision":   after.Revision,
			"updated_at": after.UpdatedAt,
		})
	if result.Error != nil {
		return fmt.Errorf("update content document revision: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		var current documentRow
		load := tx.WithContext(ctx).Select("revision").Where("id = ?", after.ID).Take(&current)
		if load.Error != nil {
			return fmt.Errorf("load current content document revision: %w", load.Error)
		}
		return staleRevision(current.Revision)
	}
	return nil
}

func (repository) deleteDocumentLocale(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	locale string,
) (int64, error) {
	result := tx.WithContext(ctx).
		Where("locale = ? AND block_id IN (?)", locale,
			tx.Model(&blockRow{}).Select("id").Where("document_id = ?", documentID),
		).
		Delete(&blockLocaleRow{})
	if result.Error != nil {
		return 0, fmt.Errorf("delete content document locale: %w", result.Error)
	}
	return result.RowsAffected, nil
}

func (repository) deleteDocument(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
) error {
	result := tx.WithContext(ctx).Where("id = ?", documentID).Delete(&documentRow{})
	if result.Error != nil {
		return fmt.Errorf("delete content document: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrDocumentNotFound
	}
	return nil
}

func documentFromRow(row documentRow) Document {
	return Document(row)
}

func uniqueUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
