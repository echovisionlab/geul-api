package public

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/memberref"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/google/uuid"
)

type FileService interface {
	ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error)
	ResolveAuthorizedContentBlockMedia(context.Context, uuid.UUID) ([]*fileref.BlockMediaItem, error)
}

type MemberSummaryLoader interface {
	LoadPublicMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error)
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

// Localizer picks the displayed locale of Posts and their Series outside
// any read snapshot: resolving a selection may store the default
// localization settings.
type Localizer interface {
	ResolveSelection(ctx context.Context, entityType, entityID, acceptLanguage string, requireNonBlankTitle bool) (LocalizedContentSelection, error)
	ResolveSelections(ctx context.Context, entityType string, entityIDs []string, acceptLanguage string, requireNonBlankTitle bool) (map[string]LocalizedContentSelection, error)
}
