package public

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediamime"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

func projectReadyPublicAsset(cdnDomain string, row ReadyAsset) (*mediaasset.Reference, error) {
	if strings.TrimSpace(row.AssetID) == "" || strings.TrimSpace(row.Extension) == "" || strings.TrimSpace(row.MimeType) == "" {
		return nil, errors.New("ready public asset metadata is incomplete")
	}
	if row.FileSize <= 0 || len(row.SHA256) != 32 {
		return nil, errors.New("ready public asset integrity metadata is incomplete")
	}
	if expected := mediamime.GetExtensionFromMime(row.MimeType); expected == "bin" || expected != row.Extension {
		return nil, errors.New("ready public asset extension does not match MIME type")
	}
	if row.Kind == "attachment" || row.Disposition != "inline" {
		return nil, errors.New("attachment public assets cannot be emitted")
	}
	assetPath, err := mediaauth.AssetPath(row.AssetID, row.Kind, row.Extension)
	if err != nil {
		return nil, err
	}
	return &mediaasset.Reference{
		AssetID:     row.AssetID,
		URL:         joinPublicCDNPath(cdnDomain, assetPath),
		Extension:   row.Extension,
		MimeType:    row.MimeType,
		FileSize:    row.FileSize,
		SHA256:      append([]byte(nil), row.SHA256...),
		Disposition: mediaasset.DispositionInline,
	}, nil
}

func readyAssetRefByID(ctx context.Context, store Store, cdnDomain string, assetID string) (*mediaasset.Reference, error) {
	row, err := store.ReadyAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	return projectReadyPublicAsset(cdnDomain, row)
}

func resolvedOgAssetRef(
	ctx context.Context,
	store Store,
	cdnDomain string,
	sourceAssetID *string,
	localizedAssetID *string,
) (*mediaasset.Reference, error) {
	for _, assetID := range []*string{localizedAssetID, sourceAssetID} {
		if assetID == nil || strings.TrimSpace(*assetID) == "" {
			continue
		}
		ref, err := readyAssetRefByID(ctx, store, cdnDomain, *assetID)
		if err == nil {
			return ref, nil
		}
		if !errors.Is(err, ErrAssetNotFound) {
			return nil, err
		}
	}
	return nil, nil
}

// ResolveOGAsset selects the localized ready asset first and then the
// source asset. The owning public domain controls when source fallback is
// allowed by the IDs it supplies.
func (s *Service) ResolveOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*mediaasset.Reference, error) {
	var result *mediaasset.Reference
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		result, err = resolvedOgAssetRef(ctx, store, s.cdnDomain, sourceAssetID, localizedAssetID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func buildPublicHLSRef(mediaDomain string, row ReadyGeneration) (*HLSReference, error) {
	url, err := mediaauth.PublicHLSURL(mediaDomain, row.FileID, row.GenerationID, row.ManifestName)
	if err != nil {
		return nil, err
	}
	return &HLSReference{
		FileID:       row.FileID,
		GenerationID: row.GenerationID,
		URL:          url,
	}, nil
}

func joinPublicCDNPath(cdnDomain string, resourcePath string) string {
	resourcePath = "/" + strings.TrimLeft(resourcePath, "/")
	cdnDomain = strings.TrimRight(strings.TrimSpace(cdnDomain), "/")
	if cdnDomain == "" {
		return resourcePath
	}
	if !strings.HasPrefix(cdnDomain, "http://") && !strings.HasPrefix(cdnDomain, "https://") {
		cdnDomain = "https://" + cdnDomain
	}
	return cdnDomain + resourcePath
}
