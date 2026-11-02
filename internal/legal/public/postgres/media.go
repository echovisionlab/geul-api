package legalpublicpostgres

import (
	"context"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	optional "github.com/echovisionlab/geul-api/internal/optional"
	"gorm.io/gorm"

	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
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
	ReadyLocalizedOGAsset(ctx context.Context, db *gorm.DB, kind, routeID, locale string) (*mediaasset.Reference, error)
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
func asset(ref *mediaasset.Reference) *public.Asset {
	disposition := public.DispositionUnspecified
	switch optional.Value(ref).Disposition {
	case mediaasset.DispositionInline:
		disposition = public.DispositionInline
	case mediaasset.DispositionAttachment:
		disposition = public.DispositionAttachment
	}
	return &public.Asset{
		ID: optional.Value(ref).AssetID, URL: optional.Value(ref).URL, Extension: optional.Value(ref).Extension, MIMEType: optional.Value(ref).MimeType,
		FileSize: optional.Value(ref).FileSize, SHA256: optional.Value(ref).SHA256, Disposition: disposition,
		DownloadFilename: ref.DownloadFilename,
	}
}
