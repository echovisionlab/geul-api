package public

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/series"
)

// Localization is how a public Series' copy was localized for the request.
type Localization struct {
	RequestedLocale string
	DisplayedLocale string
	SourceLocale    string
	IsFallback      bool
	IsOriginal      bool
	// FallbackReason is numbered as the API's LocalizationFallbackReason.
	FallbackReason   int32
	AvailableLocales []string
}

// Series is a published Series as public readers see it.
type Series struct {
	ID                 string
	Title              string
	Slug               string
	Description        *string
	PostCount          int32
	OgAsset            *series.Asset
	FeaturedImageAsset *series.Asset
	Localization       Localization
}

// Page is the public Series query result after storage filtering and
// projection have been completed by the concrete adapter.
type Page struct {
	Items  []Series
	Total  int32
	Limit  int32
	Offset int32
}

// HasMore reports whether Series follow the page.
func (p Page) HasMore() bool {
	return p.Offset+int32(len(p.Items)) < p.Total
}

// GetSeriesQuery reads one published Series by ID or slug.
type GetSeriesQuery struct {
	IDOrSlug       string
	AcceptLanguage string
}

// Reader owns the concrete public Series query and localization projection.
type Reader interface {
	List(context.Context, ListSeriesQuery) (Page, error)
	Get(context.Context, string, string) (Series, error)
}

// Service holds the public Series use cases.
type Service struct {
	reader Reader
}

// NewService returns the public Series use cases over reader.
func NewService(reader Reader) (*Service, error) {
	if reader == nil {
		return nil, errors.New("public series reader is required")
	}
	return &Service{reader: reader}, nil
}

func (s *Service) ListSeries(ctx context.Context, query ListSeriesQuery) (Page, error) {
	return s.reader.List(ctx, query)
}

func (s *Service) GetSeries(ctx context.Context, query GetSeriesQuery) (Series, error) {
	idOrSlug := strings.TrimSpace(query.IDOrSlug)
	if idOrSlug == "" {
		return Series{}, domainerr.Required("slug")
	}
	return s.reader.Get(ctx, idOrSlug, query.AcceptLanguage)
}
