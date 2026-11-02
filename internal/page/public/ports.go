package public

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// DraftAccessChecker owns the authenticated Page draft-view decision.
type DraftAccessChecker interface {
	CanViewPageDraft(context.Context, string) (bool, error)
}

// MediaResolver is the narrow public File projection required after Page
// visibility and exact attachment selectors have been authorized.
type MediaResolver interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
	ResolvePageFeaturedImageDelivery(context.Context, *string) (*commonv1.MediaDelivery, error)
	ResolvePageOGAsset(context.Context, *string, *string) (*mediaasset.Reference, error)
}

// Store is what the public Page read reads, bound to one read snapshot.
// pagepublicpostgres implements it; a missing Page is page.ErrPageNotFound.
type Store interface {
	// Audience is what the Page's access policy reads about the reader.
	pageaccess.Audience
	// HomepagePageID reads the Page the site settings make the homepage,
	// empty when there is none.
	HomepagePageID(ctx context.Context) (string, error)
	// RuntimeSettings reads the localization settings, storing the
	// defaults when none exist.
	RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// FindPage share-locks and reads the Page whose locator column equals
	// value.
	FindPage(ctx context.Context, locator page.Locator, value string) (page.Page, error)
	// DocumentRoot share-locks the Page and reads its content document.
	DocumentRoot(ctx context.Context, pageID string) (page.DocumentRoot, error)
	// SourceMetadata reads the Page's source-locale metadata and reports
	// whether it exists.
	SourceMetadata(ctx context.Context, pageID string) (page.SourceMetadata, bool, error)
	// ContentSnapshot reads the content document in the snapshot.
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// ResolveLocalization picks the displayed locale for acceptLanguage
	// among the source locale and the target locales whose Blocks
	// documentLocales holds.
	ResolveLocalization(ctx context.Context, pageID, acceptLanguage string, documentLocales map[string]struct{}, settings translation.RuntimeSettings) (publiccontent.Selection, error)
	// ResolveOGConsistency falls back to a locale whose OG image ready
	// reports ready.
	ResolveOGConsistency(ctx context.Context, pageID string, selection publiccontent.Selection, ready func(context.Context, string) (bool, error)) (publiccontent.Selection, error)
	// MediaReferences lists the document's Block media.
	MediaReferences(ctx context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)
	// RequireShareLink validates a draft share link of the Page; a missing,
	// invalid or expired link answers not found.
	RequireShareLink(ctx context.Context, token, password, pageID string) (*mediaasset.ContentDownloadShareLinkWitness, error)
}

// PublicText resolves the site variables of public text, outside the read
// snapshot.
type PublicText interface {
	ResolvePublicText(ctx context.Context, text string) (string, error)
}
