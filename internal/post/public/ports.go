package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FileService interface {
	ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)
	ResolveAuthorizedContentBlockMedia(context.Context, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)
}

// MapPlaceProjector supplies the public projection for a Post's referenced map place.
// Reference Catalog owns that projection and composition injects its adapter here.
type MapPlaceProjector interface {
	Basic(*model.MapPlace) *openv1.MapPlaceBasic
}

type MemberSummaryLoader interface {
	LoadPublicMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

type LocalizedContentSelection struct {
	RequestedLocale      string
	DisplayedLocale      string
	SourceLocale         string
	AvailableLocales     []string
	IsFallback           bool
	IsOriginal           bool
	FallbackReason       openv1.LocalizationFallbackReason
	Title                *string
	Summary              *string
	ContentJSON          []byte
	ContentHTML          *string
	ContentText          *string
	OgAssetID            *string
	OmitSourceOgFallback bool
}

func (selection LocalizedContentSelection) ProtoInfo() *openv1.LocalizationInfo {
	return &openv1.LocalizationInfo{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, IsFallback: selection.IsFallback,
		IsOriginal:     selection.IsOriginal,
		FallbackReason: selection.FallbackReason, AvailableLocales: selection.AvailableLocales,
	}
}

type LocalizationService interface {
	ResolveSelectionWithPolicy(
		context.Context, *gorm.DB, string, string, string, bool,
	) (LocalizedContentSelection, error)
	ResolveSelectionsWithPolicy(
		context.Context, *gorm.DB, string, []string, string, bool,
	) (map[string]LocalizedContentSelection, error)
	ResolveOgConsistency(
		context.Context, *gorm.DB, string, string, string, LocalizedContentSelection,
	) (LocalizedContentSelection, error)
}
