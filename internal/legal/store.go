package legal

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// The outcomes a Store answers with in place of a driver error.
var (
	// ErrPolicyNotFound is a missing version row.
	ErrPolicyNotFound = errors.New("legal policy not found")
	// ErrTranslationNotFound is a missing target locale row.
	ErrTranslationNotFound = errors.New("legal policy translation not found")
)

// Store is what the Terms and Privacy use cases read and write, bound to one
// transaction or read snapshot. Every method names the policy kind whose
// tables it touches. legalpostgres implements it over the history and
// translation tables and the shared content, OG, notice, Share Link, session
// and Member runtimes a policy transaction drives; legaltest is the
// in-memory fake.
type Store interface {
	Versions
	Translations
	ContentDocuments
	Routes
	Notices
	PreviewLinks
	Collaboration
}

// RowLock is the lock a read takes on the rows it returns.
type RowLock uint8

const (
	NoLock RowLock = iota
	ShareLock
	UpdateLock
)

// Version is one row of terms_history or privacy_history; the kinds share
// the columns.
type Version struct {
	ID                string
	Version           int
	Title             string
	Status            Status
	ContentDocumentID *string
	EffectiveFrom     *time.Time
	EffectiveUntil    *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Root is the part of a version its content document use cases read.
type Root struct {
	ID                string
	Title             string
	Status            Status
	Version           int
	SourceLocale      string
	ContentDocumentID *uuid.UUID
}

// Draft is a new draft version. Its number follows both the history and
// SealedVersion, the highest number a notice run sealed.
type Draft struct {
	Title             string
	SealedVersion     int
	ContentDocumentID string
	SourceLocale      string
	CreatedAt         time.Time
}

// Lifecycle is the lifecycle state of a version.
type Lifecycle struct {
	Status        Status
	EffectiveFrom *time.Time
	Version       int
}

// VersionRef names a version.
type VersionRef struct {
	ID      string
	Version int
}

// Projection is the HTML and text rebuilt from a document snapshot.
type Projection struct {
	HTML   string
	Text   string
	Digest string
}

// Versions reads and writes the version rows of a kind. A missing version
// is ErrPolicyNotFound.
type Versions interface {
	// LockVersionAssignment serializes the numbering of new versions.
	LockVersionAssignment(ctx context.Context, kind PolicyKind) error
	// Version reads one version with lock.
	Version(ctx context.Context, kind PolicyKind, id string, lock RowLock) (Version, error)
	// Root reads the content document root of a version, share-locked or
	// locked for update.
	Root(ctx context.Context, kind PolicyKind, id string, lock RowLock) (Root, error)
	// LockStatus locks the version for update and reads its status.
	LockStatus(ctx context.Context, kind PolicyKind, id string) (Status, error)
	// ScheduleState reads the status and effective time of a version.
	ScheduleState(ctx context.Context, kind PolicyKind, id string) (Lifecycle, error)
	// ListVersions counts the versions in status, every version when nil,
	// and lists one page of them, newest first.
	ListVersions(ctx context.Context, kind PolicyKind, status *Status, limit, offset int32) ([]Version, int64, error)
	// InsertDraft stores a new draft version and returns it.
	InsertDraft(ctx context.Context, kind PolicyKind, draft Draft) (Version, error)
	// Schedule stores the scheduled status and effectiveFrom; Unschedule
	// returns the version to draft without an effective time.
	Schedule(ctx context.Context, kind PolicyKind, id string, effectiveFrom, at time.Time) error
	Unschedule(ctx context.Context, kind PolicyKind, id string, at time.Time) error
	// LockScheduledIDs locks every scheduled version other than exceptID
	// for update, in ID order, and returns their IDs.
	LockScheduledIDs(ctx context.Context, kind PolicyKind, exceptID string) ([]string, error)
	// LockLifecycle locks the version for update and reports whether it
	// exists.
	LockLifecycle(ctx context.Context, kind PolicyKind, id string) (Lifecycle, bool, error)
	// LockActiveVersions locks every active version other than exceptID
	// for update, in ID order.
	LockActiveVersions(ctx context.Context, kind PolicyKind, exceptID string) ([]VersionRef, error)
	// Archive stores the archived status, effective until at.
	Archive(ctx context.Context, kind PolicyKind, id string, at time.Time) error
	// Activate stores the active status of a version whose status is one
	// of from, effective from effectiveFrom when set. A version in another
	// status is ErrPolicyNotFound.
	Activate(ctx context.Context, kind PolicyKind, id string, from []Status, effectiveFrom *time.Time, at time.Time) error
	// DeleteVersion deletes the version row.
	DeleteVersion(ctx context.Context, kind PolicyKind, id string) error
	// SetTitle stores the source title and at.
	SetTitle(ctx context.Context, kind PolicyKind, id, title string, at time.Time) error
	// ReplaceTitle stores a differing source title and at, and reports
	// whether the title changed.
	ReplaceTitle(ctx context.Context, kind PolicyKind, id, title string, at time.Time) (bool, error)
	// StoreSourceProjection stores the source locale's derived content and
	// its snapshot digest.
	StoreSourceProjection(ctx context.Context, kind PolicyKind, id string, projection Projection, at time.Time) error
}

// Translation is a version's row in one target locale.
type Translation struct {
	Locale    string
	Title     *string
	UpdatedAt time.Time
}

// Translations reads and writes the target locale rows of a kind.
type Translations interface {
	// CountTranslations counts the version's rows of locale.
	CountTranslations(ctx context.Context, kind PolicyKind, id, locale string) (int64, error)
	// Translation reads one target locale, locked for update when
	// forUpdate is set, and reports whether it exists.
	Translation(ctx context.Context, kind PolicyKind, id, locale string, forUpdate bool) (Translation, bool, error)
	// TranslationTitle reads the title of one target locale. A missing
	// locale is ErrTranslationNotFound.
	TranslationTitle(ctx context.Context, kind PolicyKind, id, locale string) (*string, error)
	// TranslationLocales lists the version's target locales.
	TranslationLocales(ctx context.Context, kind PolicyKind, id string) ([]string, error)
	// InsertTranslation, UpdateTranslation and DeleteTranslation report
	// how many rows they changed.
	InsertTranslation(ctx context.Context, kind PolicyKind, id, locale string, title *string, at time.Time) (int64, error)
	UpdateTranslation(ctx context.Context, kind PolicyKind, id, locale string, title *string, at time.Time) (int64, error)
	DeleteTranslation(ctx context.Context, kind PolicyKind, id, locale string) (int64, error)
	// StoreTargetProjection stores the derived content of a target locale.
	StoreTargetProjection(ctx context.Context, kind PolicyKind, id, locale string, projection Projection) error
}

// ContentFence runs before the content store locks or changes a version's
// document rows (contentblock.DomainFence), over the Store of that
// transaction, and names the source locale.
type ContentFence func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error)

// MetadataEffect writes the version metadata of a source write, in its
// transaction.
type MetadataEffect func(ctx context.Context, store Store) (contentblock.MetadataEffect, error)

// TargetMetadataEffect writes the target locale row of a target-locale
// batch, in its transaction.
type TargetMetadataEffect func(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error)

// ContentDocuments is a version's content document in the shared content
// store (contentblock.Store), bound to the policy transaction.
type ContentDocuments interface {
	CreateContentDocument(ctx context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error)
	// LockedContentSnapshot reads the document in the transaction;
	// ContentSnapshot reads it in one repeatable-read snapshot of its own.
	LockedContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error)
	// ContentRevision reads the document's revision.
	ContentRevision(ctx context.Context, documentID uuid.UUID) (uuid.UUID, error)
	ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence ContentFence) (contentblock.Result, error)
	ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence ContentFence, effect MetadataEffect) (contentblock.Result, error)
	ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence ContentFence, effect TargetMetadataEffect) (contentblock.Result, error)
	AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence ContentFence, effect MetadataEffect) (contentblock.AdvanceResult, error)
	DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence ContentFence) error
	// SiteVariables reads the site settings a document's variables render.
	SiteVariables(ctx context.Context) (sitevariable.Context, error)
}

// CurrentRoute is the version the kind's public route shows: the effective
// active version, else the next scheduled one.
type CurrentRoute struct {
	ID    string
	Title string
}

// Routes is the public route of a kind and its OG generation.
type Routes interface {
	// LockActivation serializes activation of the kind's public route.
	LockActivation(ctx context.Context, kind PolicyKind) error
	// CurrentRoute reads the version the route shows, nil when none.
	CurrentRoute(ctx context.Context, kind PolicyKind) (*CurrentRoute, error)
	// RequestOG plans the OG generation of a saved version that the route
	// shows, for locale or every locale; any other version plans nothing.
	RequestOG(ctx context.Context, kind PolicyKind, id, locale string, allLocales bool, reason string) error
	// ReleaseRoute cancels the route's OG generation and releases its
	// images.
	ReleaseRoute(ctx context.Context, kind PolicyKind) error
}

// NoticeRun is a sealed Campaign delivery run of a legal notice.
type NoticeRun struct {
	ID               string
	RunKind          string
	TemplateEventKey *string
	TermsID          *string
	PrivacyID        *string
	TemplateData     map[string]any
}

// Notices are the Campaign delivery runs of a kind's notices, which
// Campaign owns.
type Notices interface {
	// SealNoticeRun seals the notice run of eventKey for a version, to
	// dispatch at scheduledAt.
	SealNoticeRun(ctx context.Context, kind PolicyKind, id, eventKey string, templateData map[string]string, scheduledAt time.Time) (*NoticeRun, error)
	// LatestSealedVersion returns the highest version of kind a notice run
	// sealed, so a deleted version's number is never reused.
	LatestSealedVersion(ctx context.Context, kind PolicyKind) (int, error)
	// CancelScheduledNotices cancels the version's scheduled notices and
	// rejects the cancellation once one started sending.
	CancelScheduledNotices(ctx context.Context, kind PolicyKind, id string) error
	// StopNoticesOfDeletedVersion stops the deleted version's notices and
	// keeps their sealed definitions as history.
	StopNoticesOfDeletedVersion(ctx context.Context, kind PolicyKind, id string) error
	// UpdateNoticeRuns returns the version's update notices with their
	// template data, oldest first.
	UpdateNoticeRuns(ctx context.Context, kind PolicyKind, id string) ([]NoticeRun, error)
}

// AutomaticPreview is the password-free preview link a scheduled update
// notice mails out; it expires when the version takes effect.
type AutomaticPreview struct {
	Token      string
	EntityType string
	EntityID   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// PreviewLinks are the Share Links of versions, which the Share Link domain
// owns.
type PreviewLinks interface {
	CreateAutomaticPreview(ctx context.Context, preview AutomaticPreview) error
	DeleteAutomaticPreview(ctx context.Context, token, entityType, entityID string) error
	// DeletePreviewLinks deletes every link of a version.
	DeletePreviewLinks(ctx context.Context, entityType, entityID string) error
}

// ContentUpdate is the interactive content.updated signal of an AI document
// write.
type ContentUpdate struct {
	Kind                PolicyKind
	PolicyID            string
	Locale              string
	DocumentRevision    string
	TargetRevision      *string
	Deleted             bool
	ContributorMemberID string
}

// Collaboration is the shared session, contributor, Member, translation
// setting and signal state a policy transaction reads.
type Collaboration interface {
	// ResolveSessionPrincipal resolves a collaboration session's principal;
	// an invalid session is auth.ErrSessionPrincipalInvalid.
	ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error)
	// RequireCurrentContributors requires that the contributors may still
	// save the version's room.
	RequireCurrentContributors(ctx context.Context, kind PolicyKind, id string, contributors []string) error
	// PreferredLocale reads the live Member's preferred locale, or nil.
	PreferredLocale(ctx context.Context, memberID string) (*string, error)
	// TranslationSettings reads the translation runtime settings, storing
	// the defaults first.
	TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error)
	// PublishContentUpdate schedules the content.updated signal of update.
	PublishContentUpdate(ctx context.Context, update ContentUpdate) error
}

// Joined is a transaction a pipeline outside Legal opened, which a Legal
// step joins instead of opening its own: a scheduled activation, a notice
// dispatch, a translation apply or an interchange. Store and Audit are both
// bound to that transaction, so the step passes a nil transaction to
// domainaudit. legalpostgres.Bind binds a gorm transaction; a test joins
// the fake Store and an audittest.Recorder.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}

// LockActive locks a principal's Identity through the identity fence of
// the joined transaction and reports whether it is still active, onboarded
// and unbanned.
type LockActive func(*auth.UserInfo) (bool, error)
