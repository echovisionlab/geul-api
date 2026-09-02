package filemedia

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// finalizeFileManagerDeliveries is the final issuance fence for File Manager
// private originals. Authorization and response enrichment happen before this
// short transaction. The transaction locks File rows before ready generated-
// output relations, rechecks both witnesses, and signs locally while mutation
// and deletion are excluded. Mesh completion and File deletion use the same
// File -> candidate order.
func (s *FileService) finalizeFileManagerDeliveries(
	ctx context.Context,
	rows []fileManagerCatalogRow,
	members map[string]*commonv1.MemberSummary,
	usageCounts map[string]int32,
	generatedOutputs []*managev1.FileGeneratedOutput,
) (map[string]*managev1.FileManagerFile, []*managev1.FileGeneratedOutput, bool, error) {
	fileIDs := fileManagerDeliveryFileIDs(rows, generatedOutputs)
	if len(fileIDs) == 0 {
		return map[string]*managev1.FileManagerFile{}, cloneFileGeneratedOutputs(generatedOutputs), false, nil
	}
	if s.testBeforeFileManagerDeliveryFence != nil {
		s.testBeforeFileManagerDeliveryFence(append([]string(nil), fileIDs...))
	}

	files := make(map[string]*managev1.FileManagerFile, len(rows))
	outputs := cloneFileGeneratedOutputs(generatedOutputs)
	meshWitnesses := fileManagerMeshDeliveryWitnesses(rows, outputs)
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		active, err := identitystate.LockActivePrincipal(ctx, tx, auth.GetUser(ctx))
		if err != nil {
			return fmt.Errorf("lock File Manager delivery principal: %w", err)
		}
		if !active {
			changed = true
			return nil
		}
		var locked []model.File
		query := tx.WithContext(ctx).
			Select("id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "folder_id", "uploaded_by_member_id", "delete_requested_at", "created_at", "updated_at").
			Where("id IN ?", fileIDs).
			Order("id ASC")
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "SHARE"})
		}
		if err := query.Find(&locked).Error; err != nil {
			return err
		}
		lockedByID := make(map[string]model.File, len(locked))
		for _, file := range locked {
			lockedByID[file.ID] = file
		}
		currentMeshCandidates, err := lockFileManagerMeshDeliveryWitnesses(ctx, tx, meshWitnesses)
		if err != nil {
			return err
		}

		for _, row := range rows {
			if row.ItemType != "file" {
				continue
			}
			file, ok := lockedByID[row.ID]
			if !ok || file.DeleteRequestedAt != nil || !fileManagerCatalogRowMatchesFile(row, file) {
				changed = true
				continue
			}
			built, err := s.fileManagerFileFromCatalogRow(row, members, usageCounts[row.ID])
			if err != nil {
				return err
			}
			files[row.ID] = built
		}

		for _, output := range outputs {
			delivery := output.GetDelivery()
			if delivery == nil || (delivery.GetInline() == nil && delivery.GetDownload() == nil) {
				continue
			}
			if output.GetType() == managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_OPTIMIZED_MESH &&
				!currentMeshCandidates[output.GetId()] {
				output.Delivery = nil
				continue
			}
			file, ok := lockedByID[delivery.GetFileId()]
			if !ok || file.DeleteRequestedAt != nil || !fileManagerDeliveryMatchesFile(delivery, file) {
				output.Delivery = nil
				continue
			}
			fenced, err := s.rebuildLockedPrivateDelivery(delivery, file)
			if err != nil {
				return err
			}
			output.Delivery = fenced
		}
		if s.testAfterFileManagerDeliverySigned != nil {
			s.testAfterFileManagerDeliverySigned(append([]string(nil), fileIDs...))
		}
		return nil
	})
	return files, outputs, changed, err
}

type fileManagerMeshDeliveryWitness struct {
	candidateID  string
	sourceFileID string
	outputFileID string
}

func fileManagerMeshDeliveryWitnesses(
	rows []fileManagerCatalogRow,
	outputs []*managev1.FileGeneratedOutput,
) []fileManagerMeshDeliveryWitness {
	sourceFileID := ""
	for _, row := range rows {
		if row.ItemType == "file" {
			sourceFileID = row.ID
			break
		}
	}
	witnesses := make([]fileManagerMeshDeliveryWitness, 0)
	for _, output := range outputs {
		if output.GetType() != managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_OPTIMIZED_MESH ||
			output.GetDelivery() == nil || output.GetDelivery().GetInline() == nil {
			continue
		}
		witnesses = append(witnesses, fileManagerMeshDeliveryWitness{
			candidateID: output.GetId(), sourceFileID: sourceFileID, outputFileID: output.GetDelivery().GetFileId(),
		})
	}
	sort.Slice(witnesses, func(i, j int) bool { return witnesses[i].candidateID < witnesses[j].candidateID })
	return witnesses
}

func lockFileManagerMeshDeliveryWitnesses(
	ctx context.Context,
	tx *gorm.DB,
	witnesses []fileManagerMeshDeliveryWitness,
) (map[string]bool, error) {
	current := make(map[string]bool, len(witnesses))
	for _, witness := range witnesses {
		var row struct{ ID string }
		query := tx.WithContext(ctx).Table("mesh_optimization_candidate").Select("id").Where(
			"id = ? AND source_file_id = ? AND status = ? AND output_file_id = ?",
			witness.candidateID, witness.sourceFileID, model.MeshOptimizationCandidateStatusReady, witness.outputFileID,
		)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "SHARE"})
		}
		result := query.Take(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if result.Error != nil {
			return nil, result.Error
		}
		current[witness.candidateID] = true
	}
	return current, nil
}

func fileManagerDeliveryFileIDs(rows []fileManagerCatalogRow, outputs []*managev1.FileGeneratedOutput) []string {
	seen := make(map[string]struct{}, len(rows)+len(outputs))
	for _, row := range rows {
		if row.ItemType == "file" && strings.TrimSpace(row.ID) != "" {
			seen[row.ID] = struct{}{}
		}
	}
	for _, output := range outputs {
		delivery := output.GetDelivery()
		if delivery == nil || (delivery.GetInline() == nil && delivery.GetDownload() == nil) {
			continue
		}
		if fileID := strings.TrimSpace(delivery.GetFileId()); fileID != "" {
			seen[fileID] = struct{}{}
		}
	}
	fileIDs := make([]string, 0, len(seen))
	for fileID := range seen {
		fileIDs = append(fileIDs, fileID)
	}
	sort.Strings(fileIDs)
	return fileIDs
}

func cloneFileGeneratedOutputs(outputs []*managev1.FileGeneratedOutput) []*managev1.FileGeneratedOutput {
	cloned := make([]*managev1.FileGeneratedOutput, 0, len(outputs))
	for _, output := range outputs {
		if output == nil {
			cloned = append(cloned, nil)
			continue
		}
		cloned = append(cloned, proto.Clone(output).(*managev1.FileGeneratedOutput))
	}
	return cloned
}

func fileManagerCatalogRowMatchesFile(row fileManagerCatalogRow, file model.File) bool {
	if row.ID != file.ID || row.Name != file.FileName || !equalOptionalString(row.ParentID, file.FolderID) ||
		!equalOptionalString(row.MemberID, file.UploadedByMemberID) || row.Extension == nil ||
		row.MimeType == nil || row.FileSize == nil || *row.Extension != file.Extension ||
		*row.MimeType != file.MimeType || *row.FileSize != file.FileSize ||
		!row.CreatedAt.Equal(file.CreatedAt) || !row.UpdatedAt.Equal(file.UpdatedAt) {
		return false
	}
	if row.DurationSeconds == nil || file.DurationSeconds == nil {
		return row.DurationSeconds == nil && file.DurationSeconds == nil
	}
	return int64(*row.DurationSeconds) == int64(*file.DurationSeconds)
}

func fileManagerDeliveryMatchesFile(delivery *commonv1.MediaDelivery, file model.File) bool {
	if delivery.GetFileId() != file.ID || delivery.GetExtension() != file.Extension ||
		delivery.GetMimeType() != file.MimeType || delivery.GetFileSize() != file.FileSize {
		return false
	}
	if delivery.DurationSeconds == nil || file.DurationSeconds == nil {
		return delivery.DurationSeconds == nil && file.DurationSeconds == nil
	}
	return int64(delivery.GetDurationSeconds()) == int64(*file.DurationSeconds)
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *FileService) rebuildLockedPrivateDelivery(
	previous *commonv1.MediaDelivery,
	file model.File,
) (*commonv1.MediaDelivery, error) {
	delivery := proto.Clone(previous).(*commonv1.MediaDelivery)
	delivery.FileId = file.ID
	delivery.Extension = file.Extension
	delivery.MimeType = file.MimeType
	delivery.FileSize = file.FileSize
	delivery.FileName = optionalNonEmptyString(file.FileName)
	if file.DurationSeconds == nil {
		delivery.DurationSeconds = nil
	} else {
		duration := int32(*file.DurationSeconds)
		delivery.DurationSeconds = &duration
	}
	delivery.Inline = nil
	delivery.Download = nil
	var err error
	if previous.GetInline() != nil {
		delivery.Inline, err = buildExpiringMediaFileRef(
			s.mediaDomain, s.mediaSecret, file.ID, file.Extension, file.MimeType, nil,
			mediaauth.PurposeInline, mediaauth.InlineTTL,
		)
		if err != nil {
			return nil, err
		}
	}
	if previous.GetDownload() != nil {
		delivery.Download, err = buildExpiringMediaFileRef(
			s.mediaDomain, s.mediaSecret, file.ID, file.Extension, file.MimeType, &file.FileName,
			mediaauth.PurposeDownload, s.effectiveDownloadTTL(),
		)
		if err != nil {
			return nil, err
		}
	}
	return delivery, nil
}

// finalizeManageFileURLResponses is the post-authorization issuance fence for
// the external manage delivery RPCs. Internal content composition keeps its
// owning authorization boundary and does not pass through this method.
func (s *FileService) finalizeManageFileURLResponses(
	ctx context.Context,
	responses map[string]*managev1.GetMediaDeliveryResponse,
	authorization *manageFileDeliveryAuthorization,
) (map[string]*managev1.GetMediaDeliveryResponse, bool, error) {
	fileIDs := make([]string, 0, len(responses))
	for fileID, response := range responses {
		if strings.TrimSpace(fileID) == "" || response == nil || response.GetDelivery() == nil {
			continue
		}
		fileIDs = append(fileIDs, fileID)
	}
	sort.Strings(fileIDs)
	if len(fileIDs) == 0 {
		return map[string]*managev1.GetMediaDeliveryResponse{}, false, nil
	}
	if s.testBeforeManageDeliveryFence != nil {
		s.testBeforeManageDeliveryFence(append([]string(nil), fileIDs...))
	}

	if authorization == nil || authorization.principal == nil {
		return map[string]*managev1.GetMediaDeliveryResponse{}, true, nil
	}
	weakIDs := make([]string, 0, len(fileIDs))
	strongIDs := make([]string, 0, len(fileIDs))
	changed := false
	for _, fileID := range fileIDs {
		grant, ok := authorization.files[fileID]
		if !ok {
			changed = true
			continue
		}
		if grant.lockFile {
			strongIDs = append(strongIDs, fileID)
		} else {
			weakIDs = append(weakIDs, fileID)
		}
	}

	result := make(map[string]*managev1.GetMediaDeliveryResponse, len(fileIDs))
	weak, weakChanged, err := s.finalizeUsageManageFileURLResponses(ctx, weakIDs, responses, authorization)
	if err != nil {
		return nil, changed, err
	}
	for fileID, response := range weak {
		result[fileID] = response
	}
	changed = changed || weakChanged
	strong, strongChanged, err := s.finalizeStrongManageFileURLResponses(ctx, strongIDs, responses, authorization)
	if err != nil {
		return nil, changed, err
	}
	for fileID, response := range strong {
		result[fileID] = response
	}
	changed = changed || strongChanged
	return result, changed, nil
}

func (s *FileService) finalizeStrongManageFileURLResponses(
	ctx context.Context,
	fileIDs []string,
	responses map[string]*managev1.GetMediaDeliveryResponse,
	authorization *manageFileDeliveryAuthorization,
) (map[string]*managev1.GetMediaDeliveryResponse, bool, error) {
	result := make(map[string]*managev1.GetMediaDeliveryResponse, len(fileIDs))
	if len(fileIDs) == 0 {
		return result, false, nil
	}
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.testBeforeManagePrincipalLock != nil {
			s.testBeforeManagePrincipalLock(append([]string(nil), fileIDs...))
		}
		active, err := identitystate.LockActivePrincipal(ctx, tx, authorization.principal)
		if err != nil {
			return fmt.Errorf("lock manage delivery principal: %w", err)
		}
		if !active {
			changed = true
			return nil
		}
		var locked []model.File
		query := tx.WithContext(ctx).
			Select("id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "ingest_slot_id", "ingest_attempt_id", "uploaded_by_member_id", "delete_requested_at").
			Where("id IN ?", fileIDs).
			Order("id ASC")
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "SHARE"})
		}
		if err := query.Find(&locked).Error; err != nil {
			return err
		}
		lockedByID := make(map[string]model.File, len(locked))
		for _, file := range locked {
			lockedByID[file.ID] = file
		}
		for _, fileID := range fileIDs {
			response := responses[fileID]
			grant := authorization.files[fileID]
			file, ok := lockedByID[fileID]
			if !ok || file.DeleteRequestedAt != nil || !manageDeliveryResponseMatchesFile(response, file) ||
				(grant.expectedUploader != "" && (file.UploadedByMemberID == nil || *file.UploadedByMemberID != grant.expectedUploader)) {
				changed = true
				continue
			}
			if grant.expectedAvatarOwner != "" {
				var binding model.FileIngestBinding
				bindingQuery := tx.WithContext(ctx).Where(
					"file_id = ? AND upload_type = ? AND entity_id = ?",
					fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(), grant.expectedAvatarOwner,
				)
				if tx.Dialector.Name() == "postgres" {
					bindingQuery = bindingQuery.Clauses(clause.Locking{Strength: "SHARE"})
				}
				if err := bindingQuery.Take(&binding).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						changed = true
						continue
					}
					return err
				}
			}
			cloned := proto.Clone(response).(*managev1.GetMediaDeliveryResponse)
			fenced, err := s.rebuildLockedPrivateDelivery(cloned.GetDelivery(), file)
			if err != nil {
				return err
			}
			cloned.Delivery = fenced
			result[fileID] = cloned
		}
		if s.testAfterManageDeliverySigned != nil {
			s.testAfterManageDeliverySigned(append([]string(nil), fileIDs...))
		}
		return nil
	})
	return result, changed, err
}

func (s *FileService) finalizeUsageManageFileURLResponses(
	ctx context.Context,
	fileIDs []string,
	responses map[string]*managev1.GetMediaDeliveryResponse,
	authorization *manageFileDeliveryAuthorization,
) (map[string]*managev1.GetMediaDeliveryResponse, bool, error) {
	result := make(map[string]*managev1.GetMediaDeliveryResponse, len(fileIDs))
	changed := false
	for _, fileID := range fileIDs {
		response, current, err := s.finalizeUsageManageFileURLResponse(ctx, fileID, responses[fileID], authorization)
		if err != nil {
			return nil, changed, err
		}
		if !current {
			changed = true
			continue
		}
		result[fileID] = response
	}
	return result, changed, nil
}

func (s *FileService) finalizeUsageManageFileURLResponse(
	ctx context.Context,
	fileID string,
	response *managev1.GetMediaDeliveryResponse,
	authorization *manageFileDeliveryAuthorization,
) (*managev1.GetMediaDeliveryResponse, bool, error) {
	var finalized *managev1.GetMediaDeliveryResponse
	currentResult := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		witnesses := append([]manageFileDeliveryUsageWitness(nil), authorization.files[fileID].usageWitnesses...)
		sort.Slice(witnesses, func(i, j int) bool { return witnesses[i].key() < witnesses[j].key() })
		currentOwners, err := lockManageDeliveryUsageOwners(ctx, tx, witnesses)
		if err != nil {
			return err
		}
		active, err := identitystate.LockActivePrincipal(ctx, tx, authorization.principal)
		if err != nil {
			return fmt.Errorf("lock usage manage delivery principal: %w", err)
		}
		if !active {
			return nil
		}
		current := make(map[string]bool, len(witnesses))
		for _, witness := range witnesses {
			matches, err := lockExactManageDeliveryUsage(ctx, tx, witness)
			if err != nil {
				return err
			}
			current[witness.key()] = matches
		}
		var file model.File
		fileQuery := tx.WithContext(ctx).
			Select("id", "file_name", "extension", "mime_type", "file_size", "duration_seconds", "ingest_slot_id", "ingest_attempt_id", "delete_requested_at").
			Where("id = ?", fileID)
		if tx.Dialector.Name() == "postgres" {
			fileQuery = fileQuery.Clauses(clause.Locking{Strength: "SHARE"})
		}
		if err := fileQuery.Take(&file).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		grant := authorization.files[fileID]
		anyCurrent := false
		for _, witness := range grant.usageWitnesses {
			ownerKey := witness.resourceType + "\x00" + witness.resourceID
			anyCurrent = anyCurrent || (currentOwners[ownerKey] && current[witness.key()])
		}
		if !anyCurrent || file.DeleteRequestedAt != nil || !manageDeliveryResponseMatchesFile(response, file) {
			return nil
		}
		cloned := proto.Clone(response).(*managev1.GetMediaDeliveryResponse)
		previous := proto.Clone(cloned.GetDelivery()).(*commonv1.MediaDelivery)
		previous.Download = nil
		fenced, err := s.rebuildLockedPrivateDelivery(previous, file)
		if err != nil {
			return err
		}
		cloned.Delivery = fenced
		finalized = cloned
		currentResult = true
		if s.testAfterManageDeliverySigned != nil {
			s.testAfterManageDeliverySigned([]string{fileID})
		}
		return nil
	})
	return finalized, currentResult, err
}

func lockManageDeliveryUsageOwners(
	ctx context.Context,
	tx *gorm.DB,
	witnesses []manageFileDeliveryUsageWitness,
) (map[string]bool, error) {
	owners := make(map[string]manageFileDeliveryUsageWitness)
	for _, witness := range witnesses {
		owners[witness.resourceType+"\x00"+witness.resourceID] = witness
	}
	keys := make([]string, 0, len(owners))
	for key := range owners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	current := make(map[string]bool, len(keys))
	for _, key := range keys {
		witness := owners[key]
		table, ok := uploadPermissionResourceTable(witness.resourceType)
		if !ok {
			return nil, fmt.Errorf("unsupported manage delivery usage owner %q", witness.resourceType)
		}
		var row struct {
			ID                string  `gorm:"column:id"`
			ContentDocumentID *string `gorm:"column:content_document_id"`
			Status            string  `gorm:"column:status"`
		}
		selectFields := "id"
		if isContentManageDeliveryOwner(witness.resourceType) {
			selectFields = "id, content_document_id"
			if witness.resourceType == "post" || witness.resourceType == "program_event" {
				selectFields += ", status"
			}
		}
		query := tx.WithContext(ctx).Table(table).Select(selectFields).Where("id = ?", witness.resourceID)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "SHARE"})
		}
		result := query.Take(&row)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if result.Error != nil {
			return nil, result.Error
		}
		fingerprint := ""
		if isContentManageDeliveryOwner(witness.resourceType) {
			documentID := ""
			if row.ContentDocumentID != nil {
				documentID = *row.ContentDocumentID
			}
			fingerprint = documentID + ":"
			if witness.resourceType == "post" || witness.resourceType == "program_event" {
				fingerprint += row.Status
			}
		}
		current[key] = fingerprint == witness.ownerFingerprint
	}
	return current, nil
}

func isContentManageDeliveryOwner(resourceType string) bool {
	switch resourceType {
	case "page", "post", "program_event", "work":
		return true
	default:
		return false
	}
}

func lockExactManageDeliveryUsage(
	ctx context.Context,
	tx *gorm.DB,
	witness manageFileDeliveryUsageWitness,
) (bool, error) {
	query := tx.WithContext(ctx)
	if tx.Dialector.Name() == "postgres" {
		query = query.Clauses(clause.Locking{Strength: "SHARE"})
	}
	var row struct{ ID string }
	switch witness.kind {
	case "content_attachment":
		result := query.Table("content_block_attachment").Select("block_id AS id").
			Where("block_id = ? AND reference_path = ? AND selector_kind = 'active' AND file_id = ?", witness.relationID, witness.referencePath, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "featured_image":
		table, ok := uploadPermissionResourceTable(witness.resourceType)
		if !ok {
			return false, fmt.Errorf("unsupported featured image owner %q", witness.resourceType)
		}
		result := query.Table(table).Select("id").Where("id = ? AND featured_image_file_id = ?", witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "artist_file":
		result := query.Table("artist_file").Select("artist_id AS id").Where("artist_id = ? AND file_id = ?", witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "release_file":
		result := query.Table("release_file").Select("release_id AS id").Where("release_id = ? AND file_id = ?", witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "program_event_media":
		result := query.Table("program_event_media").Select("id").Where("id = ? AND event_id = ? AND file_id = ?", witness.relationID, witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "track":
		result := query.Table("track").Select("id").Where("id = ? AND release_id = ? AND audio_original_file_id = ?", witness.relationID, witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "label_logo":
		column := "logo_light_file_id"
		if witness.referencePath == "logo_dark" {
			column = "logo_dark_file_id"
		}
		result := query.Table("label").Select("id").Where("id = ? AND "+column+" = ?", witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "series_featured_image":
		result := query.Table("series").Select("id").Where("id = ? AND featured_image_file_id = ?", witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	case "form_featured_image":
		result := query.Table("form").Select("id").Where("id = ? AND featured_image_file_id = ?", witness.resourceID, witness.fileID).Take(&row)
		return manageDeliveryUsageLockResult(result)
	default:
		return false, fmt.Errorf("unsupported manage delivery usage witness %q", witness.kind)
	}
}

func manageDeliveryUsageLockResult(result *gorm.DB) (bool, error) {
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if result.Error != nil {
		return false, result.Error
	}
	return true, nil
}

func manageDeliveryResponseMatchesFile(response *managev1.GetMediaDeliveryResponse, file model.File) bool {
	if response == nil || !fileManagerDeliveryMatchesFile(response.GetDelivery(), file) ||
		!equalOptionalString(response.IngestSlotId, file.IngestSlotID) ||
		!equalOptionalString(response.IngestAttemptId, file.IngestAttemptID) {
		return false
	}
	fileName := response.GetDelivery().FileName
	if fileName == nil {
		return strings.TrimSpace(file.FileName) == ""
	}
	return *fileName == file.FileName
}
