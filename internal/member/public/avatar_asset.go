package public

import (
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	mediaauth "github.com/echovisionlab/geul-mediaauth"
)

type readyPublicAssetRow struct {
	AssetID     string `gorm:"column:asset_id"`
	Kind        string `gorm:"column:kind"`
	Extension   string `gorm:"column:extension"`
	MimeType    string `gorm:"column:mime_type"`
	FileSize    int64  `gorm:"column:file_size"`
	SHA256      []byte `gorm:"column:sha256"`
	Disposition string `gorm:"column:disposition"`
}

func readyPublicAssetSelect(alias string) string {
	return fmt.Sprintf(`
		%s.id AS asset_id, %s.kind, %s.extension, %s.mime_type,
		%s.file_size, %s.sha256, %s.disposition
	`, alias, alias, alias, alias, alias, alias, alias)
}

func projectReadyPublicAsset(cdnDomain string, row readyPublicAssetRow) (*commonv1.AssetRef, error) {
	if strings.TrimSpace(row.AssetID) == "" || strings.TrimSpace(row.Extension) == "" ||
		strings.TrimSpace(row.MimeType) == "" {
		return nil, errors.New("ready public asset metadata is incomplete")
	}
	if row.FileSize <= 0 || len(row.SHA256) != 32 || row.Disposition != "inline" {
		return nil, errors.New("ready public asset integrity metadata is incomplete")
	}
	if expected := model.GetExtensionFromMime(row.MimeType); expected == "bin" || expected != row.Extension {
		return nil, errors.New("ready public asset extension does not match MIME type")
	}
	assetPath, err := mediaauth.AssetPath(row.AssetID, row.Kind, row.Extension)
	if err != nil {
		return nil, err
	}
	return &commonv1.AssetRef{
		AssetId:     row.AssetID,
		Url:         strings.TrimRight(cdnDomain, "/") + assetPath,
		Extension:   row.Extension,
		MimeType:    row.MimeType,
		FileSize:    row.FileSize,
		Sha256:      append([]byte(nil), row.SHA256...),
		Disposition: commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	}, nil
}

func uniqueNonEmptyIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			result = append(result, id)
		}
	}
	return result
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
