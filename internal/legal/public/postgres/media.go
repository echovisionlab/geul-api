package legalpublicpostgres

import (
	"context"

	"gorm.io/gorm"

	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// localizationSpec is where a version's localized title lives.
func localizationSpec(kind legaldomain.PolicyKind) publiccontent.Spec {
	entity := string(kind)
	return publiccontent.Spec{EntityType: entity, TableName: entity + "_translation", SelectClause: "locale, title"}
}

// Localizer resolves the displayed locale of a version on the pool, outside
// any snapshot, because resolving may store the default localization
// settings.
type Localizer struct {
	db *gorm.DB
}

// NewLocalizer returns the public localization of versions over db.
func NewLocalizer(db *gorm.DB) Localizer {
	return Localizer{db: db}
}

var _ public.Localizer = Localizer{}

func (l Localizer) Resolve(ctx context.Context, kind legaldomain.PolicyKind, entityID, acceptLanguage string) (publiccontent.Selection, error) {
	return publiccontent.Resolve(ctx, l.db, localizationSpec(kind), entityID, acceptLanguage)
}

func (l Localizer) FallbackToSource(ctx context.Context, kind legaldomain.PolicyKind, entityID string, selection publiccontent.Selection) (publiccontent.Selection, error) {
	return publiccontent.FallbackToSource(ctx, l.db, localizationSpec(kind), entityID, selection)
}

// Media is the OG generation and asset projection of the public routes;
// adapters/legal.Runtime implements it.
type Media interface {
	RouteID(kind string) string
	LocalizedOGDisposition(ctx context.Context, db *gorm.DB, kind, routeID, locale string) (public.OGDisposition, error)
	ReadyLocalizedOGAsset(ctx context.Context, db *gorm.DB, kind, routeID, locale string) (*commonv1.AssetRef, error)
}

// OGImages reads the OG images of the public routes on the pool.
type OGImages struct {
	db    *gorm.DB
	media Media
}

// NewOGImages returns the OG images of the public routes over db.
func NewOGImages(db *gorm.DB, media Media) OGImages {
	return OGImages{db: db, media: media}
}

var _ public.OGImages = OGImages{}

func (o OGImages) LocalizedOGDisposition(ctx context.Context, kind legaldomain.PolicyKind, locale string) (public.OGDisposition, error) {
	return o.media.LocalizedOGDisposition(ctx, o.db, string(kind), o.media.RouteID(string(kind)), locale)
}

func (o OGImages) ReadyLocalizedOGImage(ctx context.Context, kind legaldomain.PolicyKind, locale string) (*public.Asset, error) {
	ref, err := o.media.ReadyLocalizedOGAsset(ctx, o.db, string(kind), o.media.RouteID(string(kind)), locale)
	if err != nil || ref == nil {
		return nil, err
	}
	return asset(ref), nil
}

// asset maps a ready asset reference to the public domain's asset.
func asset(ref *commonv1.AssetRef) *public.Asset {
	disposition := public.DispositionUnspecified
	switch ref.GetDisposition() {
	case commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE:
		disposition = public.DispositionInline
	case commonv1.AssetDisposition_ASSET_DISPOSITION_ATTACHMENT:
		disposition = public.DispositionAttachment
	}
	return &public.Asset{
		ID: ref.GetAssetId(), URL: ref.GetUrl(), Extension: ref.GetExtension(), MIMEType: ref.GetMimeType(),
		FileSize: ref.GetFileSize(), SHA256: ref.GetSha256(), Disposition: disposition,
		DownloadFilename: ref.DownloadFilename,
	}
}
