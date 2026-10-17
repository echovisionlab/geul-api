package mediaasset

import (
	"context"
	"strings"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
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
) (*commonv1.AssetRef, string, error) {
	ref, err := s.ReadyAssetRefForSourceFile(ctx, sourceFileID, brandingKind)
	if err == nil || connect.CodeOf(err) != connect.CodeNotFound ||
		(brandingKind != BrandingKindLogo && brandingKind != BrandingKindEmailImage) {
		return ref, brandingKind, err
	}
	ref, err = s.ReadyAssetRefForSourceFile(ctx, sourceFileID, "image")
	if err != nil {
		return nil, brandingKind, err
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(ref.GetMimeType(), ";")[0]))
	if mimeType != "image/png" && (brandingKind != BrandingKindLogo || mimeType != "image/svg+xml") {
		return nil, brandingKind, errs.InvalidArgument("file_id", "branding requires PNG or SVG; email branding requires PNG")
	}
	return ref, "image", nil
}
