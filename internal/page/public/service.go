package public

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Deps are the collaborators of the public Page use cases; all are
// required.
type Deps struct {
	Units       mutation.UnitOfWork[Store] // pagepublicpostgres.NewUnits
	DraftAccess DraftAccessChecker
	Media       MediaResolver
	Text        PublicText
	// Permissions decide the reader's roles for a Page access policy.
	Permissions pageaccess.PermissionChecker
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "DraftAccess": d.DraftAccess == nil,
		"Media": d.Media == nil, "Text": d.Text == nil, "Permissions": d.Permissions == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public page dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Page use cases.
type Service struct {
	units       mutation.UnitOfWork[Store]
	draftAccess DraftAccessChecker
	media       MediaResolver
	text        PublicText
	permissions pageaccess.PermissionChecker
}

// NewService validates deps and returns the public Page use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{units: deps.Units, draftAccess: deps.DraftAccess, media: deps.Media, text: deps.Text, permissions: deps.Permissions}, nil
}

// GetPageQuery reads the Page Slug names. An empty slug or "/" reads the
// homepage; a UUID reads by ID. ShareToken and SharePassword admit a draft;
// AcceptLanguage picks its localization.
type GetPageQuery struct {
	Slug           string
	ShareToken     *string
	SharePassword  string
	AcceptLanguage string
}

// errPageNotFound answers a Page the reader may not know exists.
var errPageNotFound = domainerr.New(domainerr.NotFound, "", "page not found")

type publicPageRead struct {
	locator         page.Locator
	value           string
	publishedOnly   bool
	notFoundAsEmpty bool
	shareToken      *string
	sharePassword   string
}

// Read is a public Page read: why the reader may or may not see the Page
// and, when Visible, the Page in its displayed locale.
type Read struct {
	AccessReason pageaccess.Reason
	Visible      bool
	Page         page.Page
	// Title and Summary are the displayed locale's.
	Title         string
	Summary       *string
	Document      *contentv1.LocalizedPageDocument
	Revision      string
	Localization  publiccontent.Selection
	OgAsset       *mediaasset.Reference
	FeaturedImage *fileref.Delivery
	BlockMedia    []*contentv1.ContentBlockMediaItem
}

// GetPage reads the public projection of a Page.
// - slug="/" or "" returns homepage (static page or latest_posts mode)
// - slug="about" returns the page with that slug
// - share_token allows access to draft pages
// - authenticated accounts with the exact SpiceDB view permission can access drafts
func (s *Service) GetPage(ctx context.Context, query GetPageQuery) (Read, error) {
	if query.Slug == "/" || query.Slug == "" {
		return s.handleHomepage(ctx, query.AcceptLanguage)
	}
	return s.handlePageBySlugOrID(ctx, query.Slug, query.ShareToken, query.SharePassword, query.AcceptLanguage)
}

// handleHomepage handles requests for the homepage (slug="/")
func (s *Service) handleHomepage(ctx context.Context, acceptLanguage string) (Read, error) {
	var homepagePageID string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		homepagePageID, err = store.HomepagePageID(ctx)
		return err
	}); err != nil {
		return Read{}, err
	}
	if homepagePageID == "" {
		// No homepage configured
		return Read{}, nil
	}
	return s.buildPageRead(ctx, acceptLanguage, publicPageRead{
		locator:         page.ByID,
		value:           homepagePageID,
		publishedOnly:   true,
		notFoundAsEmpty: true,
	})
}

// handlePageBySlugOrID handles requests for pages by slug or ID
// UUID-first: if input is valid UUID, query by ID; otherwise query by slug
func (s *Service) handlePageBySlugOrID(
	ctx context.Context,
	slugOrID string,
	shareToken *string,
	sharePassword string,
	acceptLanguage string,
) (Read, error) {
	locator := page.BySlug
	if _, err := uuid.Parse(slugOrID); err == nil {
		locator = page.ByID
	}
	return s.buildPageRead(ctx, acceptLanguage, publicPageRead{
		locator:       locator,
		value:         slugOrID,
		shareToken:    shareToken,
		sharePassword: sharePassword,
	})
}

// buildPageRead builds one coherent localized Page projection.
func (s *Service) buildPageRead(ctx context.Context, acceptLanguage string, read publicPageRead) (Read, error) {
	if read.locator != page.ByID && read.locator != page.BySlug {
		return Read{}, errors.New("page read locator is invalid")
	}
	var settings translation.RuntimeSettings
	settingsErr := s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		settings, err = store.RuntimeSettings(ctx)
		return err
	})
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
		return Read{}, err
	}
	if !result.visible {
		return Read{AccessReason: result.accessReason}, nil
	}
	view, err := s.localizedView(ctx, &result)
	if err != nil {
		return Read{}, err
	}
	if view.BlockMedia, err = s.media.HydrateAuthorizedContentBlockMedia(
		mediaasset.WithContentDownloadOwnerAuthorization(ctx, result.mediaAuthorization), result.blockMedia,
	); err != nil {
		return Read{}, err
	}
	if view.FeaturedImage, err = s.media.ResolvePageFeaturedImageDelivery(ctx, result.page.FeaturedImageFileID); err != nil {
		return Read{}, err
	}
	return view, nil
}

// pageReadResult is what one repeatable-read attempt observed. visible is
// false when the Page is absent from an empty-on-miss read or its access
// policy denies the reader; accessReason then says why.
type pageReadResult struct {
	page               page.Page
	visible            bool
	accessReason       pageaccess.Reason
	mediaAuthorization mediaasset.ContentDownloadOwnerAuthorization
	sourceMetadata     page.SourceMetadata
	localization       publiccontent.Selection
	snapshot           contentblock.Snapshot
	document           *contentv1.LocalizedPageDocument
	blockMedia         []*contentv1.ContentBlockMediaItem
}

func (s *Service) loadPageRead(
	ctx context.Context,
	read publicPageRead,
	acceptLanguage string,
	settings translation.RuntimeSettings,
) (pageReadResult, error) {
	var result pageReadResult
	err := s.units.ReadLocked(ctx, func(store Store) error {
		found, err := store.FindPage(ctx, read.locator, read.value)
		if err != nil {
			if errors.Is(err, page.ErrPageNotFound) {
				if read.notFoundAsEmpty {
					return nil
				}
				return errPageNotFound
			}
			return err
		}
		result.page = found
		result.accessReason, result.mediaAuthorization, err = s.authorizePageRead(ctx, store, read, result.page)
		if err != nil {
			return err
		}
		if result.accessReason != pageaccess.ReasonAllowed {
			return nil
		}
		result.visible = true
		return s.loadPageDocument(ctx, store, &result, acceptLanguage, settings)
	})
	return result, err
}

// authorizePageRead decides whether the reader may see page and returns the
// owner authorization its media is delivered under. A draft is visible to a
// reader with draft view permission or a valid share link; a published Page
// follows its access policy, and a denial is a reason, not an error.
func (s *Service) authorizePageRead(
	ctx context.Context,
	store Store,
	read publicPageRead,
	value page.Page,
) (pageaccess.Reason, mediaasset.ContentDownloadOwnerAuthorization, error) {
	var authorization mediaasset.ContentDownloadOwnerAuthorization
	if value.Status == page.StatusPublished {
		accessReason, err := pageaccess.Evaluate(ctx, store, s.permissions, value.ID, value.AccessPolicy)
		if err != nil {
			return accessReason, authorization, err
		}
		if accessReason == pageaccess.ReasonAllowed {
			authorization.Mode = mediaasset.ContentDownloadOwnerAccessPublic
		}
		return accessReason, authorization, nil
	}
	if read.publishedOnly {
		return pageaccess.ReasonUnspecified, authorization, errPageNotFound
	}
	allowed, err := s.draftAccess.CanViewPageDraft(ctx, value.ID)
	if err != nil {
		return pageaccess.ReasonUnspecified, authorization, fmt.Errorf("check page draft view permission: %w", err)
	}
	if allowed {
		authorization.Mode = mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft
		if user := auth.GetUser(ctx); user != nil {
			authorization.IdentityID = user.IdentityID.String()
			authorization.MemberID = user.MemberID.String()
		}
		return pageaccess.ReasonAllowed, authorization, nil
	}
	shareToken := ""
	if read.shareToken != nil {
		shareToken = *read.shareToken
	}
	link, err := store.RequireShareLink(ctx, shareToken, read.sharePassword, value.ID)
	if err != nil {
		return pageaccess.ReasonUnspecified, authorization, err
	}
	authorization.Mode = mediaasset.ContentDownloadOwnerAccessShare
	authorization.ShareLink = link
	return pageaccess.ReasonAllowed, authorization, nil
}

// loadPageDocument loads the visible Page's content snapshot, selects the
// displayed locale and materializes the document in result.
func (s *Service) loadPageDocument(
	ctx context.Context,
	store Store,
	result *pageReadResult,
	acceptLanguage string,
	settings translation.RuntimeSettings,
) error {
	value := &result.page
	documentID, err := loadContentDocumentID(ctx, store, value.ID)
	if err != nil {
		return err
	}
	result.mediaAuthorization.ResourceType = "page"
	result.mediaAuthorization.ResourceID = value.ID
	result.mediaAuthorization.Status = string(value.Status)
	result.mediaAuthorization.DocumentID = documentID.String()
	source, exists, err := store.SourceMetadata(ctx, value.ID)
	if err != nil {
		return fmt.Errorf("load Page source locale metadata: %w", err)
	}
	if !exists {
		return errors.New("page source locale metadata is not initialized")
	}
	result.sourceMetadata = source
	result.snapshot, err = store.ContentSnapshot(ctx, documentID, source.Locale)
	if err != nil {
		return fmt.Errorf("load Page content document: %w", err)
	}
	localization, err := store.ResolveLocalization(ctx, value.ID, acceptLanguage, pageDocumentLocales(result.snapshot), settings)
	if err != nil {
		return fmt.Errorf("select Page locale: %w", err)
	}
	result.localization, err = store.ResolveOGConsistency(ctx, value.ID, localization,
		func(ctx context.Context, assetID string) (bool, error) {
			asset, err := s.media.ResolvePageOGAsset(ctx, nil, &assetID)
			return asset != nil, err
		},
	)
	if err != nil {
		return fmt.Errorf("select Page OG locale: %w", err)
	}
	result.document, err = contentblock.MaterializeSnapshotPageLocale(result.snapshot, result.localization.DisplayedLocale)
	if err != nil {
		return fmt.Errorf("materialize Page content document: %w", err)
	}
	result.blockMedia, err = store.MediaReferences(ctx, documentID)
	if err != nil {
		return err
	}
	return nil
}

// loadContentDocumentID reads the Page's content document; a missing Page
// answers not found.
func loadContentDocumentID(ctx context.Context, store Store, pageID string) (uuid.UUID, error) {
	root, err := store.DocumentRoot(ctx, pageID)
	if err != nil {
		if errors.Is(err, page.ErrPageNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound("page", pageID)
		}
		return uuid.Nil, err
	}
	if root.ContentDocumentID == nil || *root.ContentDocumentID == uuid.Nil {
		return uuid.Nil, domainerr.Precondition("page content document has not been populated")
	}
	return *root.ContentDocumentID, nil
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

// localizedView renders the visible read in its displayed locale: the source
// title and summary, or the displayed locale's, and its OG image.
func (s *Service) localizedView(ctx context.Context, result *pageReadResult) (Read, error) {
	localization := result.localization
	view := Read{
		AccessReason: result.accessReason, Visible: true, Page: result.page,
		Title: derefString(result.sourceMetadata.Title), Summary: result.sourceMetadata.Summary,
		Document: result.document, Revision: result.snapshot.Document.Revision.String(), Localization: localization,
	}
	pageSourceOgAssetID := result.page.OgAssetID
	if localization.OmitSourceOgFallback {
		pageSourceOgAssetID = nil
	}
	var err error
	if view.OgAsset, err = s.media.ResolvePageOGAsset(ctx, pageSourceOgAssetID, localization.OgAssetID); err != nil {
		return Read{}, err
	}
	applyLocalizedPageFields(&view, localization)
	if view.Summary != nil && localization.IsOriginal {
		resolved, err := s.text.ResolvePublicText(ctx, *view.Summary)
		if err != nil {
			return Read{}, err
		}
		view.Summary = &resolved
	}
	return view, nil
}

func applyLocalizedPageFields(view *Read, localization publiccontent.Selection) {
	if view == nil || localization.IsOriginal {
		return
	}
	view.Title = ""
	if localization.Title != nil {
		view.Title = *localization.Title
	}
	view.Summary = localization.Summary
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
