package series

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TranslationStore is the part of the Series store the translation
// application's transaction reaches: the root lock, the source copy, the
// locale rows, the content document and the audit records of a locale
// change. internal/series/postgres binds it to that transaction.
type TranslationStore interface {
	// LockSeries locks the Series root FOR UPDATE, or reports
	// ErrSeriesNotFound.
	LockSeries(ctx context.Context, seriesID string) error

	// SourceCopies loads the source copy of each Series in seriesIDs that
	// has one, keyed by Series ID.
	SourceCopies(ctx context.Context, seriesIDs []string) (map[string]SourceCopy, error)
	// Translation reads one locale row, locked FOR UPDATE when lock is set,
	// or reports ErrTranslationNotFound.
	Translation(ctx context.Context, seriesID, locale string, lock bool) (Translation, error)
	// UpsertTranslation stores the full copy of one locale.
	UpsertTranslation(ctx context.Context, seriesID, locale string, write TranslationWrite) error
	// InsertTranslation creates a locale row.
	InsertTranslation(ctx context.Context, translation Translation) error
	// UpdateTranslation rewrites the copy of an existing locale row, or
	// reports ErrTranslationNotFound when no row matched.
	UpdateTranslation(ctx context.Context, seriesID, locale string, title, summary, contentText *string, updatedAt time.Time) error
	// DeleteTranslation deletes a locale row, or reports
	// ErrTranslationNotFound when no row matched.
	DeleteTranslation(ctx context.Context, seriesID, locale string) error
	// SetSourceLocale makes locale the source locale of the Series.
	SetSourceLocale(ctx context.Context, seriesID, locale string, now time.Time) error

	// ContentDocumentRoot reads the Series side of its content document,
	// locked FOR UPDATE when lock is set, or reports ErrSeriesNotFound.
	ContentDocumentRoot(ctx context.Context, seriesID string, lock bool) (ContentDocumentRoot, error)
	// ContentDocument reads a content document, locked FOR UPDATE when lock
	// is set, or reports ErrContentDocumentNotFound.
	ContentDocument(ctx context.Context, documentID string, lock bool) (ContentDocumentRow, error)
	// AdvanceContentDocument moves the document of seriesID from expected to
	// next, or reports ErrContentDocumentRevisionChanged.
	AdvanceContentDocument(ctx context.Context, seriesID, documentID, expected, next string, now time.Time) error

	// AppendRequestAudit and AppendMemberAudit append a record through
	// appender in the store's own transaction (domainaudit.AppendRequest,
	// domainaudit.AppendMember), for the translation application's
	// transactions, which the Series mutation pipeline does not run.
	AppendRequestAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
	AppendMemberAudit(ctx context.Context, appender domainaudit.Appender, memberID string, action sharedtelemetry.AuditAction, build domainaudit.Builder) error
}

// Store is the Series persistence of one transaction or read snapshot, and
// the collaborators of other domains that act in the same transaction. It
// never opens a transaction.
type Store interface {
	TranslationStore

	// Series reads a Series root, or reports ErrSeriesNotFound.
	Series(ctx context.Context, seriesID string) (Series, error)
	// SeriesPage reads the page of the Series in seriesIDs, newest first,
	// and how many there are.
	SeriesPage(ctx context.Context, seriesIDs []string, limit, offset int32) ([]Series, int64, error)
	// AdminSeriesPage reads a filtered, sorted page of every Series and how
	// many match.
	AdminSeriesPage(ctx context.Context, query ListSeriesAdminQuery, limit, offset int32) ([]Series, int64, error)
	// SeriesByCreation reads every Series in seriesIDs, oldest first.
	SeriesByCreation(ctx context.Context, seriesIDs []string) ([]Series, error)
	// SlugTaken reports whether another Series than excludeID has slug.
	SlugTaken(ctx context.Context, slug, excludeID string) (bool, error)
	// InsertSeries creates a Series root and returns it with its ID, or
	// reports ErrSlugTaken.
	InsertSeries(ctx context.Context, series Series) (Series, error)
	// UpdateSeriesRoot writes the non-nil slug and status, or reports
	// ErrSlugTaken.
	UpdateSeriesRoot(ctx context.Context, seriesID string, slug *string, status *Status, now time.Time) error
	// SetFeaturedImage points the Series at fileID, or at none when nil.
	SetFeaturedImage(ctx context.Context, seriesID string, fileID *string, now time.Time) error
	// DeleteSeries deletes the Series root; its manager attributions
	// cascade.
	DeleteSeries(ctx context.Context, seriesID string) error

	// CreateContentDocument creates a compact content document.
	CreateContentDocument(ctx context.Context, documentID, revision string, now time.Time) error
	// DeleteContentDocument deletes a content document, or reports
	// ErrContentDocumentNotFound.
	DeleteContentDocument(ctx context.Context, documentID string) error

	// ManagerAttributions lists the manager attributions of a Series, oldest
	// first.
	ManagerAttributions(ctx context.Context, seriesID string) ([]ManagerAttribution, error)
	ManagerAttributed(ctx context.Context, seriesID, memberID string) (bool, error)
	UpsertManagerAttribution(ctx context.Context, seriesID, memberID string, updatedAt time.Time) error
	DeleteManagerAttribution(ctx context.Context, seriesID, memberID string) error
	// LinkedMember answers whether memberID is a Member linked to a live
	// Identity (authorizationtarget.RequireLinkedMember), with the shared
	// helper's own not-found answer.
	LinkedMember(ctx context.Context, memberID string) error
	// LockManagerTarget and LockLinkedManagerTarget lock the Identity of a
	// manager change target and return it (authorizationtarget.RequireLocked,
	// RequireLockedLinked), with the shared helper's answers.
	LockManagerTarget(ctx context.Context, memberID string) (identityID string, err error)
	LockLinkedManagerTarget(ctx context.Context, memberID string) (identityID string, err error)

	// EnsureRouteAvailable answers the route registry's duplicate error when
	// a Page occupies the Series route of slug; lock takes the route lock
	// first.
	EnsureRouteAvailable(ctx context.Context, slug string, lock bool) error
	// RouteAvailable reports whether no Page occupies the Series route of
	// slug.
	RouteAvailable(ctx context.Context, slug string) (bool, error)

	// Posts is the Post port bound to the store's transaction.
	Posts() Posts

	// RequireAttachableFile locks a File the Series may attach, or reports
	// ErrFileNotFound.
	RequireAttachableFile(ctx context.Context, fileID string) error
	// BindFeaturedImage binds the ready image of fileID as the Series
	// featured image.
	BindFeaturedImage(ctx context.Context, seriesID, fileID string) (*Asset, error)
	// ReleaseFeaturedImage releases the Series featured image binding.
	ReleaseFeaturedImage(ctx context.Context, seriesID string) error
	// CancelAndReleaseOG cancels the Series OG generations and releases
	// their bindings.
	CancelAndReleaseOG(ctx context.Context, seriesID string) error
	// RequestOG requests the current OG image of one locale, or of every
	// locale, and returns the generation run it started, if any.
	RequestOG(ctx context.Context, seriesID, locale string, allLocales bool, reason string) (*string, error)
	// RenameMenuTargets and RemoveMenuTargets keep Menu links to a Series in
	// step with its slug.
	RenameMenuTargets(ctx context.Context, seriesID, oldSlug, newSlug string) error
	RemoveMenuTargets(ctx context.Context, seriesID, slug string) error
	// RequireCurrentContributors authorizes the contributors of a
	// collaboration save.
	RequireCurrentContributors(ctx context.Context, seriesID string, memberIDs []string) error
	// PublishContentSignal publishes the interactive content signal of a
	// Series locale change (contentsignal.PublishInteractive).
	PublishContentSignal(ctx context.Context, signal ContentSignal) error
}

// ContentSignal is the interactive content.updated signal of a Series locale
// change.
type ContentSignal struct {
	SeriesID         string
	Locale           string
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
	Deleted          bool
	MemberID         string
}

// Posts is the Post port of a Series transaction. Post owns these rows and
// keeps their invariants (updated_at) when their Series relation changes.
type Posts interface {
	// SeriesPosts lists the Posts of a Series in order.
	SeriesPosts(ctx context.Context, seriesID string) ([]SeriesPost, error)
	SeriesPostIDs(ctx context.Context, seriesID string) ([]string, error)
	// SeriesOf reads, and LockSeriesOf locks, the Series a Post belongs
	// to, or reports ErrPostNotFound.
	SeriesOf(ctx context.Context, postID string) (*string, error)
	LockSeriesOf(ctx context.Context, postID string) (*string, error)
	// LockPosts locks the Posts in ID order and reports how many exist.
	LockPosts(ctx context.Context, postIDs []string) (int, error)
	NextOrder(ctx context.Context, seriesID string) (int, error)
	// Assign moves a Post into a Series at order, or reports
	// ErrPostNotFound.
	Assign(ctx context.Context, postID, seriesID string, order int, now time.Time) error
	// Clear and Order report ErrPostRelationChanged for a Post that is no
	// longer in the Series.
	Clear(ctx context.Context, postID, seriesID string, now time.Time) error
	ClearAll(ctx context.Context, seriesID string, now time.Time) error
	Order(ctx context.Context, seriesID string, postIDs []string, now time.Time) error
	// RequireLockedEdit requires the principal's Post edit decision on the
	// locked Post, answering a hidden Post as not found.
	RequireLockedEdit(ctx context.Context, postID string) error
}
