package filemedia

import (
	"context"
	"fmt"
	"sort"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

// GetFileQuery reads one File of the File library.
type GetFileQuery struct {
	FileID string
}

func (s *Service) GetFile(ctx context.Context, query GetFileQuery) (*FileDetails, error) {
	if err := requireFileDownloadAuthor(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	fileID, err := normalizeFileManagerUUID(query.FileID, "file_id", false)
	if err != nil {
		return nil, err
	}
	var row fileManagerCatalogRow
	if err := s.units.Read(ctx, func(store Store) error { var err error; row, err = store.FileCatalog(ctx, *fileID); return err }); err != nil {
		return nil, err
	}

	if row.ID == "" {
		return nil, domainerr.ResourceNotFound("file", *fileID)
	}
	members := map[string]MemberSummary{}
	if row.MemberID != nil {
		memberSummaries := s.memberSummaries
		members, err = memberSummaries.Load(ctx, []string{*row.MemberID})
		if err != nil {
			return nil, err
		}
	}
	usageRows, err := s.loadLibraryFileUsages(ctx, []string{*fileID})
	if err != nil {
		return nil, err
	}
	usageRows, err = s.filterVisibleFileUsages(ctx, usageRows)
	if err != nil {
		return nil, err
	}
	summary := summarizeFileUsages(usageRows)
	generatedOutputs, err := s.loadFileGeneratedOutputs(ctx, *fileID)
	if err != nil {
		return nil, err
	}
	files, generatedOutputs, changed, err := s.finalizeFileManagerDeliveries(
		ctx,
		[]fileManagerCatalogRow{row},
		members,
		map[string]int32{*fileID: int32(len(usageRows))},
		generatedOutputs,
	)
	if err != nil {
		return nil, err
	}
	file := files[*fileID]
	if changed || file == nil {
		return nil, domainerr.ResourceNotFound("file", *fileID)
	}
	return &FileDetails{File: file, UsageSummary: summary, GeneratedOutputs: generatedOutputs}, nil
}

type fileManagerMeshOutputRow = domain.MeshDeliveryOutput

func (s *Service) loadFileGeneratedOutputs(ctx context.Context, fileID string) ([]*GeneratedOutput, error) {
	derivatives, err := s.loadStoredDerivativeDeliveries(ctx, s.units, []string{fileID})
	if err != nil {
		return nil, err
	}
	outputs := make([]*GeneratedOutput, 0, len(derivatives))
	for _, derivative := range derivatives {
		value, ok := domain.ParseDerivativeKind(derivative.Type)
		if !ok {
			continue
		}
		output := &GeneratedOutput{
			Type:   DerivativeKind(value),
			Status: derivativeProcessingStatus(derivative),
		}
		if derivative.AssetID != nil {
			output.ID = *derivative.AssetID
			if ref, refErr := s.assetRefForDerivative(derivative); refErr != nil {
				return nil, refErr
			} else if ref != nil {
				output.Delivery = &Delivery{
					FileID: fileID, Extension: ref.Extension, MimeType: ref.MimeType,
					FileSize: ref.FileSize, Asset: ref,
					ProcessingStatus: ProcessingReady,
				}
			}
		}
		if derivative.MediaGenerationID != nil {
			output.ID = *derivative.MediaGenerationID
			if ref, refErr := s.hlsPlaybackRef(fileID, derivative); refErr != nil {
				return nil, refErr
			} else if ref != nil {
				output.Delivery = &Delivery{
					FileID: fileID, Playback: ref,
					ProcessingStatus: ProcessingReady,
				}
			}
		}
		if output.ID != "" {
			outputs = append(outputs, output)
		}
	}

	var meshRows []fileManagerMeshOutputRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		meshRows, err = store.MeshDeliveryOutputs(ctx, fileID)
		return err
	}); err != nil {
		return nil, err
	}
	readyFileIDs := make([]string, 0, len(meshRows))
	for _, row := range meshRows {
		if row.Status == "ready" && row.OutputFileID != nil {
			readyFileIDs = append(readyFileIDs, *row.OutputFileID)
		}
	}
	meshDeliveries, err := s.resolveContentFileDeliveries(ctx, readyFileIDs)
	if err != nil {
		return nil, err
	}
	for _, row := range meshRows {
		output := &GeneratedOutput{
			ID: row.ID, Type: DerivativeOptimizedMesh,
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
		return outputs[i].ID < outputs[j].ID
	})
	return outputs, nil
}

func derivativeProcessingStatus(row storedDerivativeDeliveryRow) ProcessingStatus {
	if row.AssetStatus != nil {
		return fileManagerProcessingStatus(*row.AssetStatus)
	}
	if row.MediaGenerationStatus != nil {
		return fileManagerProcessingStatus(*row.MediaGenerationStatus)
	}
	return ProcessingActive
}

func fileManagerProcessingStatus(status string) ProcessingStatus {
	switch status {
	case "ready":
		return ProcessingReady
	case "failed", "retired", "cancelled", "deleted":
		return ProcessingFailed
	default:
		return ProcessingActive
	}
}

func summarizeFileUsages(rows []fileUsageRow) []*UsageView {
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
	result := make([]*UsageView, 0, len(keys))
	for _, k := range keys {
		usage := fileUsageView(byKey[k])
		usage.Count = counts[k]
		result = append(result, usage)
	}
	return result
}
