// Package favicon owns the generated favicon bundle contract and its public
// asset projection.
package favicon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

// LoadSet returns nil for a legacy favicon source without generated derivatives.
// A generated set must be complete and have verified ready public assets.
func LoadSet(ctx context.Context, store Store, sourceFileID string) (*Set, error) {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if sourceFileID == "" {
		return nil, nil
	}

	sourceMimeType, found, err := store.SourceMimeType(ctx, sourceFileID)
	if err != nil || !found {
		return nil, err
	}

	rows, err := store.FaviconDerivatives(ctx, sourceFileID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	expected := outputSpecByType()
	if len(rows) != len(requiredOutputSpecs) {
		return nil, fmt.Errorf("favicon derivative set has %d rows, want %d", len(rows), len(requiredOutputSpecs))
	}
	refs, err := validateDerivativeRefs(store, rows, expected)
	if err != nil {
		return nil, err
	}
	set, err := setFromRefs(refs)
	if err != nil {
		return nil, err
	}
	if normalizedMimeType(sourceMimeType) == "image/svg+xml" {
		iconSVG, err := readySourceAssetRef(ctx, store, sourceFileID)
		if err != nil {
			return nil, fmt.Errorf("SVG favicon source asset is not ready: %w", err)
		}
		if normalizedMimeType(iconSVG.MimeType) != "image/svg+xml" || iconSVG.Extension != "svg" {
			return nil, fmt.Errorf("SVG favicon source asset has invalid metadata")
		}
		set.IconSVG = iconSVG
	}
	return set, nil
}

func outputSpecByType() map[string]Spec {
	result := make(map[string]Spec, len(requiredOutputSpecs))
	for _, spec := range requiredOutputSpecs {
		result[spec.DerivativeType.String()] = spec
	}
	return result
}

func validateDerivativeRefs(
	store Store,
	rows []DerivativeAsset,
	expected map[string]Spec,
) (map[string]*mediaasset.Reference, error) {
	refs := make(map[string]*mediaasset.Reference, len(rows))
	for _, row := range rows {
		spec, ok := expected[row.Type]
		if !ok {
			return nil, fmt.Errorf("favicon derivative set contains unexpected type %s", row.Type)
		}
		if _, duplicate := refs[row.Type]; duplicate {
			return nil, fmt.Errorf("favicon derivative set contains duplicate type %s", row.Type)
		}
		if !validDerivativeMetadata(row, spec) {
			return nil, fmt.Errorf("favicon derivative %s has invalid ready asset metadata", row.Type)
		}
		expectedKey, err := mediaauth.AssetObjectKey(row.ID, row.Extension)
		if err != nil || expectedKey != row.ObjectKey {
			return nil, fmt.Errorf("favicon derivative %s has a non-canonical object key", row.Type)
		}
		ref, err := store.AssetRef(row)
		if err != nil {
			return nil, err
		}
		refs[row.Type] = ref
	}
	return refs, nil
}

func validDerivativeMetadata(row DerivativeAsset, spec Spec) bool {
	return row.ID != "" && row.SourceFileID == nil && row.Kind == "favicon" && row.Extension == spec.Extension &&
		normalizedMimeType(row.MimeType) == spec.MimeType && row.FileSize != nil && *row.FileSize > 0 &&
		len(row.SHA256) == sha256.Size && row.Disposition == "inline" && row.DownloadFilename == nil &&
		row.Status == AssetReady
}

func setFromRefs(refs map[string]*mediaasset.Reference) (*Set, error) {
	required := func(derivativeType DerivativeType) (*mediaasset.Reference, error) {
		ref := refs[derivativeType.String()]
		if ref == nil {
			return nil, fmt.Errorf("favicon derivative set is missing %s", derivativeType.String())
		}
		return ref, nil
	}
	types := []DerivativeType{
		ICO,
		PNG16,
		PNG32,
		PNG48,
		AppleTouch180,
		Manifest192,
		Manifest512,
	}
	resolved := make([]*mediaasset.Reference, len(types))
	for index, derivativeType := range types {
		ref, err := required(derivativeType)
		if err != nil {
			return nil, err
		}
		resolved[index] = ref
	}
	return &Set{
		IconICO:       resolved[0],
		IconPNG16:     resolved[1],
		IconPNG32:     resolved[2],
		IconPNG48:     resolved[3],
		AppleTouch180: resolved[4],
		Manifest192:   resolved[5],
		Manifest512:   resolved[6],
	}, nil
}

// Projection selects generated PNG32 for the legacy browser field while
// returning the modern generated set. It falls back to the ready source asset.
func Projection(ctx context.Context, store Store, sourceFileID string) (*mediaasset.Reference, *Set) {
	set, err := LoadSet(ctx, store, sourceFileID)
	if err != nil {
		slog.WarnContext(ctx, "Generated favicon set is invalid; serving no favicon", "domain", "favicon", "operation", "load_favicon_set", "outcome", "failed", "file_id", sourceFileID, failurecause.Attr(err))
		return nil, nil
	}
	if set != nil {
		return set.IconPNG32, set
	}
	legacy, err := readySourceAssetRef(ctx, store, sourceFileID)
	if err != nil {
		slog.WarnContext(ctx, "Favicon source asset lookup failed; serving no favicon", "domain", "favicon", "operation", "load_favicon_source", "outcome", "failed", "file_id", sourceFileID, failurecause.Attr(err))
		return nil, nil
	}
	return legacy, nil
}

func readySourceAssetRef(ctx context.Context, store Store, sourceFileID string) (*mediaasset.Reference, error) {
	asset, found, err := store.NewestReadySourceAsset(ctx, strings.TrimSpace(sourceFileID))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, domainerr.ResourceNotFound("favicon_asset_for_source_file", sourceFileID)
	}
	return store.AssetRef(asset)
}

// AssetIDs returns generated asset identities for cleanup preservation.
func AssetIDs(set *Set) map[string]struct{} {
	ids := make(map[string]struct{})
	if set == nil {
		return ids
	}
	assets := []*mediaasset.Reference{
		set.IconICO,
		set.IconPNG16,
		set.IconPNG32,
		set.IconPNG48,
		set.AppleTouch180,
		set.Manifest192,
		set.Manifest512,
		set.IconSVG,
	}
	for _, asset := range assets {
		if asset != nil && strings.TrimSpace(asset.AssetID) != "" {
			ids[asset.AssetID] = struct{}{}
		}
	}
	return ids
}

func normalizedMimeType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}

// RequestDeletion moves unbound favicon assets for a source File into the
// public-asset deletion lifecycle while preserving existing lock semantics.
func RequestDeletion(ctx context.Context, store Store, sourceFileID string) error {
	sourceFileID = strings.TrimSpace(sourceFileID)
	if sourceFileID == "" {
		return nil
	}
	derivativeCount, err := store.FaviconDerivativeCount(ctx, sourceFileID)
	if err != nil {
		return err
	}
	if derivativeCount == 0 {
		sourceAssetCount, err := store.SourceAssetCount(ctx, sourceFileID)
		if err != nil || sourceAssetCount == 0 {
			return err
		}
	}
	assets, err := store.LockDeletionAssets(ctx, sourceFileID)
	if err != nil || len(assets) == 0 {
		return err
	}
	assetIDs := make([]string, 0, len(assets))
	for _, asset := range assets {
		if asset.Kind != "favicon" {
			return domainerr.Precondition("favicon derivative references a non-favicon public asset")
		}
		assetIDs = append(assetIDs, asset.ID)
	}
	bindingCount, err := store.BindingCount(ctx, assetIDs)
	if err != nil {
		return err
	}
	if bindingCount != 0 {
		return domainerr.Precondition("favicon assets still have active bindings")
	}
	for _, asset := range assets {
		switch asset.Status {
		case AssetReady, AssetFailed:
			if err := store.RequestAssetDeletion(ctx, asset.ID); err != nil {
				return err
			}
		case AssetDeletePending, AssetDeleted:
			continue
		default:
			return domainerr.Precondition("favicon asset cannot be deleted from its current state")
		}
	}
	return nil
}
