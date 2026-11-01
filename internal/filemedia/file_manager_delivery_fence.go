package filemedia

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"

	"github.com/echovisionlab/geul-api/internal/auth"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

// finalizeFileManagerDeliveries is the final issuance fence for File Manager
// private originals. Authorization and response enrichment happen before this
// short transaction. The transaction locks File rows before ready generated-
// output relations, rechecks both witnesses, and signs locally while mutation
// and deletion are excluded. Mesh completion and File deletion use the same
// File -> candidate order.
func (s *Service) finalizeFileManagerDeliveries(
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

	var files map[string]*managev1.FileManagerFile
	outputs := cloneFileGeneratedOutputs(generatedOutputs)
	meshWitnesses := fileManagerMeshDeliveryWitnesses(rows, outputs)
	changed := false
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		active, err := tx.LockDeliveryPrincipal(ctx, auth.GetUser(ctx))
		if err != nil {
			return fmt.Errorf("lock File Manager delivery principal: %w", err)
		}
		if !active {
			changed = true
			return nil
		}
		lockedByID, err := tx.LockDeliveryFiles(ctx, fileIDs, domain.CatalogDelivery)
		if err != nil {
			return err
		}
		currentMeshCandidates, err := lockFileManagerMeshDeliveryWitnesses(ctx, tx, meshWitnesses)
		if err != nil {
			return err
		}
		files, changed, err = s.fenceFileManagerCatalogFiles(rows, lockedByID, members, usageCounts)
		if err != nil {
			return err
		}
		if err := s.fenceFileManagerGeneratedOutputs(outputs, lockedByID, currentMeshCandidates); err != nil {
			return err
		}
		if s.testAfterFileManagerDeliverySigned != nil {
			s.testAfterFileManagerDeliverySigned(append([]string(nil), fileIDs...))
		}
		return nil
	})
	if files == nil {
		files = make(map[string]*managev1.FileManagerFile, len(rows))
	}
	return files, outputs, changed, err
}

// fenceFileManagerCatalogFiles builds the catalog Files whose locked rows
// still match what the catalog read; changed reports a File that did not.
func (s *Service) fenceFileManagerCatalogFiles(
	rows []fileManagerCatalogRow,
	lockedByID map[string]File,
	members map[string]*commonv1.MemberSummary,
	usageCounts map[string]int32,
) (map[string]*managev1.FileManagerFile, bool, error) {
	files := make(map[string]*managev1.FileManagerFile, len(rows))
	changed := false
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
			return nil, changed, err
		}
		files[row.ID] = built
	}
	return files, changed, nil
}

// fenceFileManagerGeneratedOutputs re-signs each generated output delivery
// against its locked File and drops the deliveries whose File or ready mesh
// candidate changed.
func (s *Service) fenceFileManagerGeneratedOutputs(
	outputs []*managev1.FileGeneratedOutput,
	lockedByID map[string]File,
	currentMeshCandidates map[string]bool,
) error {
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
	return nil
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

func fileManagerCatalogRowMatchesFile(row fileManagerCatalogRow, file File) bool {
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

func fileManagerDeliveryMatchesFile(delivery *commonv1.MediaDelivery, file File) bool {
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

func (s *Service) rebuildLockedPrivateDelivery(
	previous *commonv1.MediaDelivery,
	file File,
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
func (s *Service) finalizeManageFileURLResponses(
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

func (s *Service) finalizeStrongManageFileURLResponses(
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
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		if s.testBeforeManagePrincipalLock != nil {
			s.testBeforeManagePrincipalLock(append([]string(nil), fileIDs...))
		}
		active, err := tx.LockDeliveryPrincipal(ctx, authorization.principal)
		if err != nil {
			return fmt.Errorf("lock manage delivery principal: %w", err)
		}
		if !active {
			changed = true
			return nil
		}
		lockedByID, err := tx.LockDeliveryFiles(ctx, fileIDs, domain.StrongDelivery)
		if err != nil {
			return err
		}
		for _, fileID := range fileIDs {
			fenced, current, err := s.fenceStrongManageDelivery(ctx, tx, responses[fileID], authorization.files[fileID], lockedByID, fileID)
			if err != nil {
				return err
			}
			if !current {
				changed = true
				continue
			}
			result[fileID] = fenced
		}
		if s.testAfterManageDeliverySigned != nil {
			s.testAfterManageDeliverySigned(append([]string(nil), fileIDs...))
		}
		return nil
	})
	return result, changed, err
}

// fenceStrongManageDelivery re-signs response against the locked File when
// the File, its uploader and, for an avatar grant, its avatar binding are
// still the ones authorization saw; current is false otherwise.
func (s *Service) fenceStrongManageDelivery(
	ctx context.Context,
	tx Store,
	response *managev1.GetMediaDeliveryResponse,
	grant manageFileDeliveryGrant,
	lockedByID map[string]File,
	fileID string,
) (*managev1.GetMediaDeliveryResponse, bool, error) {
	file, ok := lockedByID[fileID]
	if !ok || file.DeleteRequestedAt != nil || !manageDeliveryResponseMatchesFile(response, file) ||
		(grant.expectedUploader != "" && (file.UploadedByMemberID == nil || *file.UploadedByMemberID != grant.expectedUploader)) {
		return nil, false, nil
	}
	if grant.expectedAvatarOwner != "" {
		current, err := tx.LockAvatarFileBinding(ctx, fileID, grant.expectedAvatarOwner)
		if err != nil {
			return nil, false, err
		}
		if !current {
			return nil, false, nil
		}
	}
	cloned := proto.Clone(response).(*managev1.GetMediaDeliveryResponse)
	fenced, err := s.rebuildLockedPrivateDelivery(cloned.GetDelivery(), file)
	if err != nil {
		return nil, false, err
	}
	cloned.Delivery = fenced
	return cloned, true, nil
}

func (s *Service) finalizeUsageManageFileURLResponses(
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

func (s *Service) finalizeUsageManageFileURLResponse(
	ctx context.Context,
	fileID string,
	response *managev1.GetMediaDeliveryResponse,
	authorization *manageFileDeliveryAuthorization,
) (*managev1.GetMediaDeliveryResponse, bool, error) {
	var finalized *managev1.GetMediaDeliveryResponse
	currentResult := false
	err := s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		tx := unit.Store()
		witnesses := append([]manageFileDeliveryUsageWitness(nil), authorization.files[fileID].usageWitnesses...)
		sort.Slice(witnesses, func(i, j int) bool { return witnesses[i].key() < witnesses[j].key() })
		currentOwners, err := lockManageDeliveryUsageOwners(ctx, tx, witnesses)
		if err != nil {
			return err
		}
		active, err := tx.LockDeliveryPrincipal(ctx, authorization.principal)
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
		file, found, err := tx.LockUsageDeliveryFile(ctx, fileID)
		if err != nil {
			return err
		}
		if !found {
			return nil
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
	tx Store,
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
		row, found, err := tx.LockDeliveryOwner(ctx, witness.resourceType, witness.resourceID)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
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

func manageDeliveryResponseMatchesFile(response *managev1.GetMediaDeliveryResponse, file File) bool {
	if response != nil && !equalOptionalString(response.ClientMediaBundleId, file.ClientMediaBundleID) {
		return false
	}
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

func lockFileManagerMeshDeliveryWitnesses(ctx context.Context, store Store, witnesses []fileManagerMeshDeliveryWitness) (map[string]bool, error) {
	values := make([]domain.MeshDeliveryWitness, len(witnesses))
	for i, w := range witnesses {
		values[i] = domain.MeshDeliveryWitness{CandidateID: w.candidateID, SourceFileID: w.sourceFileID, OutputFileID: w.outputFileID}
	}
	return store.LockMeshDeliveryWitnesses(ctx, values)
}
func lockExactManageDeliveryUsage(ctx context.Context, store Store, w manageFileDeliveryUsageWitness) (bool, error) {
	return store.LockExactDeliveryUsage(ctx, domain.DeliveryUsageWitness{Kind: w.kind, FileID: w.fileID, ResourceType: w.resourceType, ResourceID: w.resourceID, RelationID: w.relationID, ReferencePath: w.referencePath})
}
