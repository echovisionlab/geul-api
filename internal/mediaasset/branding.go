package mediaasset

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Branding asset kinds: display logos accept PNG or SVG, email logos PNG only.
const (
	BrandingKindLogo       = "logo"
	BrandingKindEmailImage = "email_image"
)

// ReadyBrandingAssetRef resolves the ready asset for a branding slot (site logo
// or email logo). A dedicated branding upload is preferred; a File-library image
// may be used as-is when its type suits the slot, without copying its bytes. It
// returns the ref and the asset kind it was found under. Assignment, public
// settings, OG snapshots and email rendering all resolve branding through here.
func (s *Lifecycle) ReadyBrandingAssetRef(
	ctx context.Context,
	sourceFileID string,
	brandingKind string,
) (*Reference, string, error) {
	return readyBrandingAssetRef(ctx, s.ReadyAssetRefForSourceFile, sourceFileID, brandingKind)
}

func (s *ReferenceReader) ReadyBrandingAssetRef(ctx context.Context, sourceFileID, brandingKind string) (*Reference, string, error) {
	return readyBrandingAssetRef(ctx, s.ReadyAssetRefForSourceFile, sourceFileID, brandingKind)
}

func readyBrandingAssetRef(ctx context.Context, resolve func(context.Context, string, string) (*Reference, error), sourceFileID, brandingKind string) (*Reference, string, error) {
	ref, err := resolve(ctx, sourceFileID, brandingKind)
	if err == nil || !mutation.IsNotFound(err) ||
		(brandingKind != BrandingKindLogo && brandingKind != BrandingKindEmailImage) {
		return ref, brandingKind, err
	}
	ref, err = resolve(ctx, sourceFileID, "image")
	if err != nil {
		return nil, brandingKind, err
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(ref.MimeType, ";")[0]))
	if mimeType != "image/png" && (brandingKind != BrandingKindLogo || mimeType != "image/svg+xml") {
		return nil, brandingKind, domainerr.InvalidField("file_id", "branding requires PNG or SVG; email branding requires PNG")
	}
	return ref, "image", nil
}
