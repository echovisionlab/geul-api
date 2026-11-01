// Package pagepublicpostgres is the PostgreSQL store of the public Page
// read: the page tables through pagepostgres, the site homepage, the
// localization settings and selection, and the draft share link, bound to
// one read snapshot.
package pagepublicpostgres

import (
	"context"
	"errors"
	"log/slog"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	pagepostgres "github.com/echovisionlab/geul-api/internal/page/postgres"
	pagepublic "github.com/echovisionlab/geul-api/internal/page/public"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// ShareLinkAccessChecker owns validation of Page draft share-link
// credentials. Implementations must preserve the public not-found behavior
// for missing, invalid, expired, or mismatched links.
type ShareLinkAccessChecker interface {
	RequirePageShareLinkAccess(context.Context, *gorm.DB, string, string, string) (*model.ShareLink, error)
}

// NewUnits returns the read snapshots of the public Page read.
func NewUnits(db *gorm.DB, blocks *contentblock.Store, shareLinks ShareLinkAccessChecker) (mutation.UnitOfWork[pagepublic.Store], error) {
	if db == nil || blocks == nil || shareLinks == nil {
		return nil, errors.New("public page store database, content blocks and share links are required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) pagepublic.Store {
		return store{Store: pagepostgres.NewStore(tx, pagepostgres.Ports{ContentBlocks: blocks}), tx: tx, shareLinks: shareLinks}
	}), nil
}

// NewPublicText resolves site variables over db, outside any read
// snapshot.
func NewPublicText(db *gorm.DB) pagepublic.PublicText {
	return publicText{db: db}
}

type publicText struct{ db *gorm.DB }

func (t publicText) ResolvePublicText(ctx context.Context, text string) (string, error) {
	return sitevariable.ResolvePublicText(ctx, t.db, text)
}

// store reads the page tables through the Page Store.
type store struct {
	page.Store
	tx         *gorm.DB
	shareLinks ShareLinkAccessChecker
}

var _ pagepublic.Store = store{}

var pageLocalizationSpec = publiccontent.Spec{
	EntityType:   "page",
	TableName:    "page_translation",
	SelectClause: "locale, title, summary, og_asset_id",
}

func (s store) HomepagePageID(ctx context.Context) (string, error) {
	var settings model.SiteSettings
	if err := s.tx.WithContext(ctx).
		Select("homepage_page_id").
		First(&settings, "id = 1").Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	if settings.HomepagePageID == nil {
		return "", nil
	}
	return *settings.HomepagePageID, nil
}

func (s store) FindPage(ctx context.Context, locator page.Locator, value string) (page.Page, error) {
	return s.Store.FindPage(ctx, locator, value, page.ShareLock)
}

func (s store) DocumentRoot(ctx context.Context, pageID string) (page.DocumentRoot, error) {
	return s.Store.DocumentRoot(ctx, pageID, page.ShareLock)
}

func (s store) SourceMetadata(ctx context.Context, pageID string) (page.SourceMetadata, bool, error) {
	states, err := s.Store.SourceMetadata(ctx, []string{pageID})
	if err != nil {
		return page.SourceMetadata{}, false, err
	}
	state, ok := states[pageID]
	return state, ok, nil
}

func (s store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.Store.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

func (s store) MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.Store.MediaReferences(ctx, documentID)
}

func (s store) RequireShareLink(ctx context.Context, token, password, pageID string) (*mediaasset.ContentDownloadShareLinkWitness, error) {
	link, err := s.shareLinks.RequirePageShareLinkAccess(ctx, s.tx, token, password, pageID)
	if err != nil {
		return nil, err
	}
	return mediaassetpostgres.ContentDownloadShareLinkWitnessFromModel(link), nil
}

func (s store) ResolveOGConsistency(ctx context.Context, pageID string, selection publiccontent.Selection, ready func(context.Context, string) (bool, error)) (publiccontent.Selection, error) {
	return publiccontent.ResolveOGConsistency(ctx, s.tx, pageLocalizationSpec, pageID, selection, ready)
}

// ResolveLocalization reads only Page locale metadata and admits target
// locales that exist in the already-loaded Block snapshot.
func (s store) ResolveLocalization(
	ctx context.Context,
	pageID string,
	acceptLanguage string,
	documentLocales map[string]struct{},
	settings translation.RuntimeSettings,
) (publiccontent.Selection, error) {
	return resolvePageLocalizedMetadataSelection(ctx, s.tx, pageID, acceptLanguage, documentLocales, settings)
}

func resolvePageLocalizedMetadataSelection(
	ctx context.Context,
	db *gorm.DB,
	pageID string,
	acceptLanguage string,
	documentLocales map[string]struct{},
	settings translation.RuntimeSettings,
) (publiccontent.Selection, error) {
	selection, err := publiccontent.ResolveWithPolicy(
		ctx, db, pageLocalizationSpec, pageID, acceptLanguage, settings,
	)
	if err != nil {
		return publiccontent.Selection{}, err
	}
	if selection.DisplayedLocale != selection.SourceLocale {
		if _, exists := documentLocales[selection.DisplayedLocale]; !exists {
			requestedLocale := selection.RequestedLocale
			// Re-resolving with the default locale yields its row only when it is
			// stored; otherwise ResolveWithPolicy already falls back to source.
			selection, err = publiccontent.ResolveWithPolicy(
				ctx, db, pageLocalizationSpec, pageID,
				publiccontent.DocumentFallbackLocale(selection, documentLocales, settings), settings,
			)
			if err != nil {
				return publiccontent.Selection{}, err
			}
			selection.RequestedLocale = requestedLocale
			selection.IsFallback = requestedLocale != selection.DisplayedLocale
			selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
		}
	}
	if available, availableErr := publiccontent.AvailableLocales(
		ctx, db, pageLocalizationSpec, pageID, selection.SourceLocale, settings,
	); availableErr == nil {
		selection.AvailableLocales = filterPageAvailableLocales(available, selection.SourceLocale, documentLocales)
	} else {
		slog.ErrorContext(ctx, "Available locale lookup failed; serving Page without its locale list", "domain", "page", "operation", "load_available_locales", "outcome", "failed", failurecause.Attr(availableErr), "page_id", pageID)
	}
	return selection, nil
}

func filterPageAvailableLocales(
	available []string,
	sourceLocale string,
	documentLocales map[string]struct{},
) []string {
	filtered := make([]string, 0, len(available))
	for _, locale := range available {
		if locale == sourceLocale {
			filtered = append(filtered, locale)
			continue
		}
		if _, exists := documentLocales[locale]; exists {
			filtered = append(filtered, locale)
		}
	}
	return filtered
}
