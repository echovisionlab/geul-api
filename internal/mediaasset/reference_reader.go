package mediaasset

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
)

// ReferenceStore reads immutable assets from a pool or an existing transaction.
type ReferenceStore interface {
	Asset(context.Context, string) (Asset, error)
	ReadyAssetForFile(context.Context, string, string) (Asset, error)
}

// ReferenceReader resolves ready references without opening a transaction.
type ReferenceReader struct {
	store     ReferenceStore
	cdnDomain string
}

func NewReferenceReader(store ReferenceStore, cdnDomain string) *ReferenceReader {
	return &ReferenceReader{store: store, cdnDomain: cdnDomain}
}

func (s *ReferenceReader) ReadyAssetRef(ctx context.Context, assetID string) (*Reference, error) {
	asset, err := s.store.Asset(ctx, strings.TrimSpace(assetID))
	if errors.Is(err, ErrAssetNotFound) {
		return nil, domainerr.ResourceNotFound("public_asset", strings.TrimSpace(assetID))
	}
	if err != nil {
		return nil, err
	}
	if asset.Status != AssetReady || asset.FileSize == nil || len(asset.SHA256) != 32 {
		return nil, domainerr.Precondition("public asset is not ready")
	}
	return assetReference(s.cdnDomain, asset)
}

func (s *ReferenceReader) ReadyAssetRefForSourceFile(ctx context.Context, sourceFileID, expectedKind string) (*Reference, error) {
	expectedKind = strings.TrimSpace(expectedKind)
	if _, ok := publicAssetKinds[expectedKind]; !ok {
		return nil, domainerr.InvalidField("kind", "unsupported public asset kind")
	}
	asset, err := s.store.ReadyAssetForFile(ctx, sourceFileID, expectedKind)
	if errors.Is(err, ErrAssetNotFound) {
		return nil, domainerr.ResourceNotFound("public_asset_for_source_file", sourceFileID)
	}
	if err != nil {
		return nil, err
	}
	return assetReference(s.cdnDomain, asset)
}

func assetReference(cdnDomain string, asset Asset) (*Reference, error) {
	if asset.Kind == "attachment" || asset.Disposition != "inline" {
		return nil, domainerr.Precondition("attachment public assets cannot be emitted")
	}
	assetPath, err := mediaauth.AssetPath(asset.ID, asset.Kind, asset.Extension)
	if err != nil {
		return nil, err
	}
	return &Reference{
		AssetID:     asset.ID,
		URL:         mediaauth.JoinOrigin(cdnDomain, assetPath),
		Extension:   asset.Extension,
		MimeType:    asset.MimeType,
		FileSize:    *asset.FileSize,
		SHA256:      append([]byte(nil), asset.SHA256...),
		Disposition: DispositionInline,
	}, nil
}
