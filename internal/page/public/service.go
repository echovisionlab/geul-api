package public

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/publiccontent"
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
}

type PageServiceOption func(*PageService)

type publicPageRead struct {
	column          string
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
			selection, err = publiccontent.ResolveWithPolicy(
				ctx, db, pageLocalizationSpec, pageID, selection.SourceLocale, settings,
			)
			if err != nil {
				return publiccontent.Selection{}, err
			}
			selection.RequestedLocale = requestedLocale
			selection.IsFallback = requestedLocale != selection.SourceLocale
			selection.FallbackReason = openv1.LocalizationFallbackReason_LOCALIZATION_FALLBACK_REASON_SOURCE
		}
	}
	if available, availableErr := publiccontent.AvailableLocales(
		ctx, db, pageLocalizationSpec, pageID, selection.SourceLocale, settings,
	); availableErr == nil {
		selection.AvailableLocales = filterPageAvailableLocales(available, selection.SourceLocale, documentLocales)
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
		column:          "id",
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
	column := "slug"
	if _, err := uuid.Parse(slugOrID); err == nil {
		column = "id"
	}
	return s.buildPageResponse(ctx, acceptLanguage, publicPageRead{
		column:        column,
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
	if read.column != "id" && read.column != "slug" {
		return nil, errs.InternalMsg("Page read locator is invalid")
	}
	if s.blocks == nil {
		return nil, errs.InternalMsg("Page content Block store is not configured")
	}
	settings, settingsErr := translation.LoadRuntimeSettings(ctx, s.db)
	if settingsErr != nil {
		settings = translation.DefaultRuntimeSettings()
	}

	var sourceMetadata *pagedomain.PageSourceLocaleMetadata
	var localization publiccontent.Selection
	var snapshot contentblock.Snapshot
	var document *contentv1.LocalizedPageDocument
	var blockMedia []*contentv1.ContentBlockMediaItem
	var page model.Page
	var mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	visible := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "SHARE"}).
			Where(read.column+" = ?", read.value).
			Take(&page).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if read.notFoundAsEmpty {
					return nil
				}
				return errs.NotFoundMsg("page not found")
			}
			return errs.Internal(err)
		}
		if read.publishedOnly && page.Status != model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()) {
			return nil
		}
		if !read.publishedOnly && page.Status != model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()) {
			allowed, permissionErr := s.draftAccess.CanViewPageDraft(ctx, page.ID)
			if permissionErr != nil {
				return errs.Internal(fmt.Errorf("check page draft view permission: %w", permissionErr))
			}
			if !allowed {
				shareToken := ""
				if read.shareToken != nil {
					shareToken = *read.shareToken
				}
				link, shareErr := s.shareLinks.RequirePageShareLinkAccess(
					ctx, tx, shareToken, read.sharePassword, page.ID,
				)
				if shareErr != nil {
					return shareErr
				}
				mediaAuthorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
				mediaAuthorization.ShareLink = mediaasset.ContentDownloadShareLinkWitnessFromModel(link)
			} else {
				mediaAuthorization.Mode = mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft
				if user := auth.GetUser(ctx); user != nil {
					mediaAuthorization.IdentityID = user.IdentityID.String()
					mediaAuthorization.MemberID = user.MemberID.String()
				}
			}
		} else {
			mediaAuthorization.Mode = mediaasset.ContentDownloadOwnerAccessPublic
		}
		visible = true

		documentID, err := pagedomain.LoadPageContentDocumentIDForPublicRead(ctx, tx, page.ID)
		if err != nil {
			return err
		}
		mediaAuthorization.ResourceType = "page"
		mediaAuthorization.ResourceID = page.ID
		mediaAuthorization.Status = string(page.Status)
		mediaAuthorization.DocumentID = documentID.String()
		sourceMetadata, err = pagedomain.LoadPageSourceLocaleMetadataForPublic(ctx, tx, page.ID)
		if err != nil {
			return errs.Internal(fmt.Errorf("load Page source locale metadata: %w", err))
		}
		if sourceMetadata == nil {
			return errs.InternalMsg("Page source locale metadata is not initialized")
		}
		snapshot, err = s.blocks.LoadSnapshotInTransaction(
			ctx,
			tx,
			documentID,
			sourceMetadata.Locale,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("load Page content document: %w", err))
		}
		localization, err = resolvePageLocalizedMetadataSelection(
			ctx,
			tx,
			page.ID,
			acceptLanguage,
			pageDocumentLocales(snapshot),
			settings,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("select Page locale: %w", err))
		}
		localization, err = publiccontent.ResolveOGConsistency(
			ctx, tx, pageLocalizationSpec, page.ID, localization,
			func(ctx context.Context, assetID string) (bool, error) {
				asset, err := s.media.ResolvePageOGAsset(ctx, nil, &assetID)
				return asset != nil, err
			},
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("select Page OG locale: %w", err))
		}
		document, err = pagedomain.MaterializeLocalizedPageContentDocument(
			snapshot,
			localization.DisplayedLocale,
		)
		if err != nil {
			return errs.Internal(fmt.Errorf("materialize Page content document: %w", err))
		}
		blockMedia, err = pagedomain.LoadContentBlockMediaReferences(ctx, tx, documentID)
		if err != nil {
			return errs.Internal(err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if !visible {
		return connect.NewResponse(&openv1.GetPageResponse{}), nil
	}
	pagedomain.OverlayPageSourceLocaleMetadataForPublic(&page, sourceMetadata)

	protoPage := &openv1.Page{
		Id:             page.ID,
		Title:          page.Title,
		ShowTitle:      page.ShowTitle,
		Status:         s.toProtoStatus(page.Status),
		Document:       document,
		Revision:       snapshot.Document.Revision.String(),
		DocumentLayout: page.DocumentLayout.Proto(),
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

	if s.media == nil {
		return nil, errs.InternalMsg("Page media resolver is not configured")
	}
	blockMedia, err = s.media.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, mediaAuthorization), blockMedia,
	)
	if err != nil {
		return nil, err
	}
	protoPage.FeaturedImageDelivery, err = s.media.ResolvePageFeaturedImageDelivery(
		ctx,
		page.FeaturedImageFileID,
	)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&openv1.GetPageResponse{
		Page:       protoPage,
		BlockMedia: blockMedia,
	}), nil
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
