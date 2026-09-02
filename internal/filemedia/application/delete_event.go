package application

import (
	"errors"
	"strings"

	mediaauth "github.com/echovisionlab/geul-mediaauth"
	"github.com/google/uuid"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func ValidateDeleteEvent(event *managev1.FileDeleteEvent) error {
	if event == nil {
		return errors.New("event is required")
	}
	fileID := strings.TrimSpace(event.GetFileId())
	if _, err := uuid.Parse(fileID); err != nil {
		return errors.New("file id must be a UUID")
	}
	original := event.GetOriginal()
	if original == nil || strings.TrimSpace(original.GetObjectKey()) == "" {
		return errors.New("canonical original target is required")
	}
	if original.GetFileId() != fileID || strings.TrimSpace(original.GetExtension()) == "" ||
		strings.TrimSpace(original.GetMimeType()) == "" {
		return errors.New("invalid original target")
	}
	expectedKey, err := mediaauth.MediaObjectKey(fileID, original.GetExtension())
	if err != nil || expectedKey != original.GetObjectKey() {
		return errors.New("non-canonical original target")
	}
	if len(event.GetAssets()) != 0 {
		return errors.New("full deletion must not include public assets")
	}
	seenAssets := make(map[string]struct{}, len(event.GetAssets()))
	for _, target := range event.GetAssets() {
		if target == nil {
			return errors.New("asset target is required")
		}
		assetID := strings.TrimSpace(target.GetAssetId())
		if _, err := uuid.Parse(assetID); err != nil {
			return errors.New("asset id must be a UUID")
		}
		if _, duplicate := seenAssets[assetID]; duplicate {
			return errors.New("duplicate asset target")
		}
		seenAssets[assetID] = struct{}{}
		expectedKey, err := mediaauth.AssetObjectKey(assetID, target.GetExtension())
		if err != nil || expectedKey != target.GetObjectKey() || strings.TrimSpace(target.GetMimeType()) == "" ||
			target.GetDisposition() != commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE || target.DownloadFilename != nil {
			return errors.New("non-canonical asset target")
		}
	}
	seenGenerations := make(map[string]struct{}, len(event.GetGenerations()))
	for _, target := range event.GetGenerations() {
		if target == nil || target.GetFileId() != fileID {
			return errors.New("generation target file mismatch")
		}
		generationID := strings.TrimSpace(target.GetGenerationId())
		if _, err := uuid.Parse(generationID); err != nil {
			return errors.New("generation id must be a UUID")
		}
		if _, duplicate := seenGenerations[generationID]; duplicate {
			return errors.New("duplicate generation target")
		}
		seenGenerations[generationID] = struct{}{}
		expectedPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
		if err != nil || expectedPrefix != target.GetObjectPrefix() {
			return errors.New("non-canonical generation target")
		}
	}
	return nil
}
