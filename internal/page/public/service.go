package public

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/dberrors"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PageService implements the public PageService
type PageService struct {
	openv1connect.UnimplementedPageServiceHandler
	db          *gorm.DB
	draftAccess DraftAccessChecker
	shareLinks  ShareLinkAccessChecker
	media       MediaResolver
	blocks      *contentblock.Store
	permissions pageaccess.PermissionChecker
}

type PageServiceOption func(*PageService)

// pageLocator names the column a public Page read matches.
type pageLocator string

const (
	pageLocatorID   pageLocator = "id"
	pageLocatorSlug pageLocator = "slug"
)

type publicPageRead struct {
	locator         pageLocator
	value           string
	publishedOnly   bool
	notFoundAsEmpty bool
	shareToken      *string
	sharePassword   string
}

func WithPageContentBlockStore(store *contentblock.Store) PageServiceOption {
	return func(s *PageService) {
		s.blocks = store
	}
}

func WithPageAccessPermissionChecker(checker pageaccess.PermissionChecker) PageServiceOption {
	return func(s *PageService) { s.permissions = checker }
}

var pageLocalizationSpec = publiccontent.Spec{
	EntityType:   "page",
	TableName:    "page_translation",
	SelectClause: "locale, title, summary, og_asset_id",
}

func applyLocalizedPageFields(page *openv1.Page, localization publiccontent.Selection) {
	if page == nil || localization.IsOriginal {
		return
	}

	page.Title = ""
	if localization.Title != nil {
		page.Title = *localization.Title
	}
	page.Summary = localization.Summary
}

// resolvePageLocalizedMetadataSelection reads only Page locale metadata and
// admits target locales that exist in the already-loaded Block snapshot.
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

func pageDocumentLocales(snapshot contentblock.Snapshot) map[string]struct{} {
	locales := make(map[string]struct{}, len(snapshot.LocaleOverlays))
	for _, overlay := range snapshot.LocaleOverlays {
		locale := strings.TrimSpace(overlay.Locale)
		if locale != "" {
			locales[locale] = struct{}{}
		}
	}
	return locales
}

// NewPageService creates a new public PageService
func NewPageService(
	db *gorm.DB,
	draftAccess DraftAccessChecker,
	shareLinks ShareLinkAccessChecker,
	media MediaResolver,
	options ...PageServiceOption,
) *PageService {
	if db == nil || draftAccess == nil || shareLinks == nil || media == nil {
		panic("public page service dependencies are required")
	}
	service := &PageService{
		db:          db,
		draftAccess: draftAccess,
		shareLinks:  shareLinks,
		media:       media,
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

// Get retrieves a page by slug
// - slug="/" or "" returns homepage (static page or latest_posts mode)
// - slug="about" returns the page with that slug
// - share_token allows access to draft pages
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *PageService) Get(
	ctx context.Context,
	req *connect.Request[openv1.GetPageRequest],
) (*connect.Response[openv1.GetPageResponse], error) {
	slug := req.Msg.Slug
	shareToken := req.Msg.ShareToken
	sharePassword := ""
	if req.Msg.SharePassword != nil {
		sharePassword = req.Msg.GetSharePassword()
	}
	acceptLanguage := req.Header().Get("Accept-Language")

	// Case 1: Homepage request
	if slug == "/" || slug == "" {
		return s.handleHomepage(ctx, acceptLanguage)
	}

	// Case 2: Regular page request (by slug or ID)
	return s.handlePageBySlugOrID(ctx, slug, shareToken, sharePassword, acceptLanguage)
}

// handleHomepage handles requests for the homepage (slug="/")
func (s *PageService) handleHomepage(
	ctx context.Context,
	acceptLanguage string,
) (*connect.Response[openv1.GetPageResponse], error) {
	homepagePageID, err := s.getHomepagePageID(ctx)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if homepagePageID == "" {
		// No homepage configured
		return connect.NewResponse(&openv1.GetPageResponse{}), nil
	}

	return s.buildPageResponse(ctx, acceptLanguage, publicPageRead{
		locator:         pageLocatorID,
		value:           homepagePageID,
		publishedOnly:   true,
		notFoundAsEmpty: true,
	})
}

// handlePageBySlugOrID handles requests for pages by slug or ID
// UUID-first: if input is valid UUID, query by ID; otherwise query by slug
func (s *PageService) handlePageBySlugOrID(
	ctx context.Context,
	slugOrID string,
	shareToken *string,
	sharePassword string,
	acceptLanguage string,
) (*connect.Response[openv1.GetPageResponse], error) {
	locator := pageLocatorSlug
	if _, err := uuid.Parse(slugOrID); err == nil {
		locator = pageLocatorID
	}
	return s.buildPageResponse(ctx, acceptLanguage, publicPageRead{
		locator:       locator,
		value:         slugOrID,
		shareToken:    shareToken,
		sharePassword: sharePassword,
	})
}

// buildPageResponse builds one coherent localized Page projection.
func (s *PageService) buildPageResponse(
	ctx context.Context,
	acceptLanguage string,
	read publicPageRead,
) (*connect.Response[openv1.GetPageResponse], error) {
	if read.locator != pageLocatorID && read.locator != pageLocatorSlug {
		return nil, errs.InternalMsg("Page read locator is invalid")
	}
	if s.blocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}
	settings, settingsErr := translation.LoadRuntimeSettings(ctx, s.db)
	if settingsErr != nil {
		slog.ErrorContext(ctx, "Localization settings lookup failed; serving Page with default settings", "domain", "page", "operation", "load_localization_settings", "outcome", "failed", failurecause.Attr(settingsErr))
		settings = translation.DefaultRuntimeSettings()
	}

	var result pageReadResult
	// A concurrent Page write aborts this repeatable read (SQLSTATE 40001); the
	// read is retried after it commits, starting from empty state each time.
	err := dberrors.RetrySerializationFailure(ctx, func() error {
		var err error
		result, err = s.loadPageRead(ctx, read, acceptLanguage, settings)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !result.visible {
		return connect.NewResponse(&openv1.GetPageResponse{AccessReason: result.accessReason}), nil
	}
	protoPage, err := s.toOpenPage(ctx, &result)
	if err != nil {
		return nil, err
	}
	blockMedia, err := s.hydratePageMedia(ctx, protoPage, result)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&openv1.GetPageResponse{
		AccessReason: result.accessReason,
		Page:         protoPage,
		BlockMedia:   blockMedia,
	}), nil
}

// pageReadResult is what one repeatable-read attempt observed. visible is
// false when the Page is absent from an empty-on-miss read or its access
// policy denies the reader; accessReason then says why.
type pageReadResult struct {
	page               model.Page
	visible            bool
	accessReason       commonv1.PageAccessReason
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	sourceMetadata     *pagedomain.PageSourceLocaleMetadata
	localization       publiccontent.Selection
	snapshot           contentblock.Snapshot
	document           *contentv1.LocalizedPageDocument
	blockMedia         []*contentv1.ContentBlockMediaItem
}

func (s *PageService) loadPageRead(
	ctx context.Context,
	read publicPageRead,
	acceptLanguage string,
	settings translation.RuntimeSettings,
) (pageReadResult, error) {
	var result pageReadResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
			Where(string(read.locator)+" = ?", read.value).
			Take(&result.page).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if read.notFoundAsEmpty {
					return nil
				}
				return errs.NotFoundMsg("page not found")
			}
			return errs.Internal(err)
		}
		var err error
		result.accessReason, result.mediaAuthorization, err = s.authorizePageRead(ctx, tx, read, result.page)
		if err != nil {
			return err
		}
		if result.accessReason != commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED {
			return nil
		}
		result.visible = true
		return s.loadPageDocument(ctx, tx, &result, acceptLanguage, settings)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return result, err
}

// authorizePageRead decides whether the reader may see page and returns the
// owner authorization its media is delivered under. A draft is visible to a
// reader with draft view permission or a valid share link; a published Page
// follows its access policy, and a denial is a reason, not an error.
func (s *PageService) authorizePageRead(
	ctx context.Context,
	tx *gorm.DB,
	read publicPageRead,
	page model.Page,
) (commonv1.PageAccessReason, mediaasset.ContentDownloadOwnerAuthorization, error) {
	var authorization mediaasset.ContentDownloadOwnerAuthorization
	published := page.Status == model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String())
	if published {
		accessReason, err := pageaccess.Evaluate(ctx, tx, s.permissions, page.ID, page.AccessPolicy)
		if err != nil {
			return accessReason, authorization, errs.Internal(err)
		}
		if accessReason == commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED {
			authorization.Mode = mediaasset.ContentDownloadOwnerAccessPublic
		}
		return accessReason, authorization, nil
	}
	if read.publishedOnly {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, authorization, errs.NotFoundMsg("page not found")
	}
	allowed, err := s.draftAccess.CanViewPageDraft(ctx, page.ID)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, authorization, errs.Internal(fmt.Errorf("check page draft view permission: %w", err))
	}
	if allowed {
		authorization.Mode = mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft
		if user := auth.GetUser(ctx); user != nil {
			authorization.IdentityID = user.IdentityID.String()
			authorization.MemberID = user.MemberID.String()
		}
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, authorization, nil
	}
	shareToken := ""
	if read.shareToken != nil {
		shareToken = *read.shareToken
	}
	link, err := s.shareLinks.RequirePageShareLinkAccess(ctx, tx, shareToken, read.sharePassword, page.ID)
	if err != nil {
		return commonv1.PageAccessReason_PAGE_ACCESS_REASON_UNSPECIFIED, authorization, err
	}
	authorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
	authorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
	return commonv1.PageAccessReason_PAGE_ACCESS_REASON_ALLOWED, authorization, nil
}

// loadPageDocument loads the visible Page's content snapshot, selects the
// displayed locale and materializes the document in result.
func (s *PageService) loadPageDocument(
	ctx context.Context,
	tx *gorm.DB,
	result *pageReadResult,
	acceptLanguage string,
	settings translation.RuntimeSettings,
) error {
	page := &result.page
	documentID, err := pagedomain.LoadPageContentDocumentIDForPublicRead(ctx, tx, page.ID)
	if err != nil {
		return err
	}
	result.mediaAuthorization.ResourceType = "page"
	result.mediaAuthorization.ResourceID = page.ID
	result.mediaAuthorization.Status = string(page.Status)
	result.mediaAuthorization.DocumentID = documentID.String()
	result.sourceMetadata, err = pagedomain.LoadPageSourceLocaleMetadataForPublic(ctx, tx, page.ID)
	if err != nil {
		return errs.Internal(fmt.Errorf("load Page source locale metadata: %w", err))
	}
	if result.sourceMetadata == nil {
		return errs.InternalMsg("Page source locale metadata is not initialized")
	}
	result.snapshot, err = s.blocks.LoadSnapshotInTransaction(ctx, tx, documentID, result.sourceMetadata.Locale)
	if err != nil {
		return errs.Internal(fmt.Errorf("load Page content document: %w", err))
	}
	localization, err := resolvePageLocalizedMetadataSelection(
		ctx,
		tx,
		page.ID,
		acceptLanguage,
		pageDocumentLocales(result.snapshot),
		settings,
	)
	if err != nil {
		return errs.Internal(fmt.Errorf("select Page locale: %w", err))
	}
	result.localization, err = publiccontent.ResolveOGConsistency(
		ctx, tx, pageLocalizationSpec, page.ID, localization,
		func(ctx context.Context, assetID string) (bool, error) {
			asset, err := s.media.ResolvePageOGAsset(ctx, nil, &assetID)
			return asset != nil, err
		},
	)
	if err != nil {
		return errs.Internal(fmt.Errorf("select Page OG locale: %w", err))
	}
	result.document, err = pagedomain.MaterializeLocalizedPageContentDocument(
		result.snapshot,
		result.localization.DisplayedLocale,
	)
	if err != nil {
		return errs.Internal(fmt.Errorf("materialize Page content document: %w", err))
	}
	result.blockMedia, err = pagedomain.LoadContentBlockMediaReferences(ctx, tx, documentID)
	if err != nil {
		return errs.Internal(err)
	}
	return nil
}

// toOpenPage maps a visible read to the public Page, overlaying source locale
// metadata on result.page and resolving its OG asset and localized summary.
func (s *PageService) toOpenPage(ctx context.Context, result *pageReadResult) (*openv1.Page, error) {
	page := &result.page
	localization := result.localization
	accessPolicy, err := pageaccess.Decode(page.AccessPolicy)
	if err != nil {
		return nil, errs.Internal(err)
	}
	pagedomain.OverlayPageSourceLocaleMetadataForPublic(page, result.sourceMetadata)
	documentLayout, err := page.DocumentLayout.Proto()
	if err != nil {
		return nil, errs.Internal(err)
	}

	protoPage := &openv1.Page{
		AccessPolicy:   accessPolicy,
		Id:             page.ID,
		Title:          page.Title,
		ShowTitle:      page.ShowTitle,
		Status:         s.toProtoStatus(page.Status),
		Document:       result.document,
		Revision:       result.snapshot.Document.Revision.String(),
		DocumentLayout: documentLayout,
		CreatedAt:      timestamppb.New(page.CreatedAt),
		UpdatedAt:      timestamppb.New(page.UpdatedAt),
	}

	if page.Slug != nil {
		protoPage.Slug = page.Slug
	}
	if page.Summary != nil {
		protoPage.Summary = page.Summary
	}
	protoPage.LocalizationInfo = publiccontent.ToProtoLocalizationInfo(localization)

	if page.PublishedAt != nil {
		protoPage.PublishedAt = timestamppb.New(*page.PublishedAt)
	}
	pageSourceOgAssetID := page.OgAssetID
	if localization.OmitSourceOgFallback {
		pageSourceOgAssetID = nil
	}
	protoPage.OgAsset, err = s.media.ResolvePageOGAsset(ctx, pageSourceOgAssetID, localization.OgAssetID)
	if err != nil {
		return nil, errs.Internal(err)
	}

	applyLocalizedPageFields(protoPage, localization)
	if protoPage.Summary != nil && localization.IsOriginal {
		resolved, resolveErr := sitevariable.ResolvePublicText(ctx, s.db, *protoPage.Summary)
		if resolveErr != nil {
			return nil, errs.Internal(resolveErr)
		}
		protoPage.Summary = &resolved
	}
	return protoPage, nil
}

// hydratePageMedia authorizes the read's content Block media under its owner
// authorization and sets the featured image delivery on protoPage.
func (s *PageService) hydratePageMedia(
	ctx context.Context,
	protoPage *openv1.Page,
	result pageReadResult,
) ([]*contentv1.ContentBlockMediaItem, error) {
	blockMedia, err := s.media.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, result.mediaAuthorization), result.blockMedia,
	)
	if err != nil {
		return nil, err
	}
	protoPage.FeaturedImageDelivery, err = s.media.ResolvePageFeaturedImageDelivery(ctx, result.page.FeaturedImageFileID)
	if err != nil {
		return nil, err
	}
	return blockMedia, nil
}

func (s *PageService) getHomepagePageID(ctx context.Context) (string, error) {
	var settings model.SiteSettings
	if err := s.db.WithContext(ctx).
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

// toProtoStatus converts model page status to proto status
func (s *PageService) toProtoStatus(status model.PageStatus) openv1.PageStatus {
	switch status {
	case model.PageStatus(managev1.PageStatus_PAGE_STATUS_DRAFT.String()):
		return openv1.PageStatus_PAGE_STATUS_DRAFT
	case model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()):
		return openv1.PageStatus_PAGE_STATUS_PUBLISHED
	default:
		return openv1.PageStatus_PAGE_STATUS_UNSPECIFIED
	}
}
