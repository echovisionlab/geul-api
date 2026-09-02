package filemedia

import (
	"context"
	"fmt"
	"sort"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) GetFile(
	ctx context.Context,
	req *connect.Request[managev1.GetFileRequest],
) (*connect.Response[managev1.GetFileResponse], error) {
	if _, err := requireFileDownloadAuthor(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	fileID, err := normalizeFileManagerUUID(req.Msg.FileId, "file_id", false)
	if err != nil {
		return nil, err
	}
	var row fileManagerCatalogRow
	if err := s.db.WithContext(ctx).Raw(`SELECT 'file' AS item_type, id, folder_id AS parent_id, file_name AS name, extension, mime_type, file_size, duration_seconds, uploaded_by_member_id AS member_id, created_at, updated_at, 1 AS total FROM file WHERE id = ? AND delete_requested_at IS NULL`, *fileID).Scan(&row).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if row.ID == "" {
		return nil, errs.NotFound("file", *fileID)
	}
	members := map[string]*commonv1.MemberSummary{}
	if row.MemberID != nil {
		memberSummaries, dependencyErr := requireMemberSummaries(s.memberSummaries)
		if dependencyErr != nil {
			return nil, dependencyErr
		}
		members, err = memberSummaries.Load(ctx, []string{*row.MemberID})
		if err != nil {
			return nil, errs.Internal(err)
		}
	}
	usageRows, err := loadFileUsages(ctx, s.db, []string{*fileID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	usageRows, err = s.filterVisibleFileUsages(ctx, usageRows)
	if err != nil {
		return nil, errs.Internal(err)
	}
	summary := summarizeFileUsages(usageRows)
	generatedOutputs, err := s.loadFileGeneratedOutputs(ctx, *fileID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	files, generatedOutputs, changed, err := s.finalizeFileManagerDeliveries(
		ctx,
		[]fileManagerCatalogRow{row},
		members,
		map[string]int32{*fileID: int32(len(usageRows))},
		generatedOutputs,
	)
	if err != nil {
		return nil, errs.Internal(err)
	}
	file := files[*fileID]
	if changed || file == nil {
		return nil, errs.NotFound("file", *fileID)
	}
	return connect.NewResponse(&managev1.GetFileResponse{File: file, DomainUsageSummary: summary, GeneratedOutputs: generatedOutputs}), nil
}

type fileManagerMeshOutputRow struct {
	ID           string  `gorm:"column:id"`
	Status       string  `gorm:"column:status"`
	OutputFileID *string `gorm:"column:output_file_id"`
}

func (s *FileService) loadFileGeneratedOutputs(ctx context.Context, fileID string) ([]*managev1.FileGeneratedOutput, error) {
	derivatives, err := s.loadStoredDerivativeDeliveries(ctx, []string{fileID})
	if err != nil {
		return nil, err
	}
	outputs := make([]*managev1.FileGeneratedOutput, 0, len(derivatives))
	for _, derivative := range derivatives {
		value, ok := managev1.FileDerivativeType_value[derivative.Type]
		if !ok {
			continue
		}
		output := &managev1.FileGeneratedOutput{
			Type:   managev1.FileDerivativeType(value),
			Status: derivativeProcessingStatus(derivative),
		}
		if derivative.AssetID != nil {
			output.Id = *derivative.AssetID
			if ref, refErr := s.assetRefForDerivative(derivative); refErr != nil {
				return nil, refErr
			} else if ref != nil {
				output.Delivery = &commonv1.MediaDelivery{
					FileId: fileID, Extension: ref.Extension, MimeType: ref.MimeType,
					FileSize: ref.FileSize, Asset: ref,
					ProcessingStatus: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
				}
			}
		}
		if derivative.MediaGenerationID != nil {
			output.Id = *derivative.MediaGenerationID
			if ref, refErr := s.hlsPlaybackRef(fileID, derivative); refErr != nil {
				return nil, refErr
			} else if ref != nil {
				output.Delivery = &commonv1.MediaDelivery{
					FileId: fileID, Playback: ref,
					ProcessingStatus: commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY,
				}
			}
		}
		if output.Id != "" {
			outputs = append(outputs, output)
		}
	}

	var meshRows []fileManagerMeshOutputRow
	if err := s.db.WithContext(ctx).Table("mesh_optimization_candidate").
		Select("id", "status", "output_file_id").
		Where("source_file_id = ? AND output_file_id IS NOT NULL", fileID).
		Order("created_at DESC, id DESC").
		Find(&meshRows).Error; err != nil {
		return nil, err
	}
	readyFileIDs := make([]string, 0, len(meshRows))
	for _, row := range meshRows {
		if row.Status == model.MeshOptimizationCandidateStatusReady && row.OutputFileID != nil {
			readyFileIDs = append(readyFileIDs, *row.OutputFileID)
		}
	}
	meshDeliveries, err := s.resolveContentFileDeliveries(ctx, readyFileIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range meshRows {
		output := &managev1.FileGeneratedOutput{
			Id: row.ID, Type: managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_OPTIMIZED_MESH,
			Status: fileManagerProcessingStatus(row.Status),
		}
		if row.OutputFileID != nil {
			output.Delivery = meshDeliveries[*row.OutputFileID]
		}
		outputs = append(outputs, output)
	}
	sort.SliceStable(outputs, func(i, j int) bool {
		if outputs[i].Type != outputs[j].Type {
			return outputs[i].Type < outputs[j].Type
		}
		return outputs[i].Id < outputs[j].Id
	})
	return outputs, nil
}

func derivativeProcessingStatus(row storedDerivativeDeliveryRow) commonv1.MediaProcessingStatus {
	if row.AssetStatus != nil {
		return fileManagerProcessingStatus(*row.AssetStatus)
	}
	if row.MediaGenerationStatus != nil {
		return fileManagerProcessingStatus(*row.MediaGenerationStatus)
	}
	return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING
}

func fileManagerProcessingStatus(status string) commonv1.MediaProcessingStatus {
	switch status {
	case "ready":
		return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_READY
	case "failed", "retired", "cancelled", "deleted":
		return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_FAILED
	default:
		return commonv1.MediaProcessingStatus_MEDIA_PROCESSING_STATUS_PROCESSING
	}
}

func summarizeFileUsages(rows []fileUsageRow) []*managev1.FileUsage {
	type key struct{ domain, entity, slot, block string }
	counts := make(map[key]int32)
	byKey := make(map[key]fileUsageRow)
	for _, row := range rows {
		block := ""
		if row.BlockID != nil {
			block = *row.BlockID
		}
		k := key{row.Domain, row.EntityID, row.ReferencePath, block}
		counts[k]++
		byKey[k] = row
	}
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%s", keys[i].domain, keys[i].entity, keys[i].slot, keys[i].block) < fmt.Sprintf("%s\x00%s\x00%s\x00%s", keys[j].domain, keys[j].entity, keys[j].slot, keys[j].block)
	})
	result := make([]*managev1.FileUsage, 0, len(keys))
	for _, k := range keys {
		usage := fileUsageProto(byKey[k])
		usage.Count = counts[k]
		result = append(result, usage)
	}
	return result
}
