package post

import (
	"errors"
	"fmt"
	"time"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/memberref"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// Status is the stored lifecycle state of a Post. The values are the
// strings the post table holds.
type Status string

const (
	StatusDraft     Status = "POST_STATUS_DRAFT"
	StatusScheduled Status = "POST_STATUS_SCHEDULED"
	StatusPublished Status = "POST_STATUS_PUBLISHED"
	StatusArchived  Status = "POST_STATUS_ARCHIVED"
)

// Post is a stored Post with its source-locale title and summary and the
// relations a manage read shows.
type Post struct {
	ID                    string
	ContentDocumentID     *string
	SourceLocale          string
	Title                 string
	Summary               *string
	Slug                  *string
	ConfigurationRevision string
	DocumentLayout        DocumentLayout
	Status                Status
	CommentsEnabled       bool
	SeriesID              *string
	SeriesOrder           *int32
	MapPlaceID            *string
	FeaturedImageFileID   *string
	OgAssetID             *string
	SourceLocaleOgAssetID *string
	PublishedAt           *time.Time
	ScheduledAt           *time.Time
	ScheduledTimeZone     *string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// ContentRevision is the content document revision a read projected.
	ContentRevision string

	Categories []Category
	Tags       []Tag
	Series     *Series
}

// Category is a Category a Post is filed under.
type Category struct {
	ID          string
	Name        string
	Slug        string
	Description *string
	CreatedAt   time.Time
}

// Tag is a Tag a Post carries.
type Tag struct {
	ID        string
	Name      string
	Slug      string
	CreatedAt time.Time
}

// Series is the Series row a Post belongs to. Title and Description are
// what the row alone holds: a manage read does not localize them.
type Series struct {
	ID           string
	SourceLocale string
	Title        string
	Status       string
	Slug         string
	Description  *string
	CreatedAt    time.Time
}

// The values of a document layout: the strings the document_layout column
// holds. An empty value is one the API request left unspecified.
const (
	ContentHeightContent  = "content"
	ContentHeightViewport = "viewport"
	PlacementFlow         = "flow"
	PlacementPinned       = "pinned"
)

// DocumentLayout is how a Post document is laid out.
type DocumentLayout struct {
	ContentHeight       string
	PageChrome          string
	Footer              string
	HideTableOfContents bool
}

// DefaultDocumentLayout is the layout of a new Post.
func DefaultDocumentLayout() DocumentLayout {
	return DocumentLayout{ContentHeight: ContentHeightContent, PageChrome: PlacementFlow, Footer: PlacementFlow}
}

// validateRequested rejects a requested layout with a value the API left
// unspecified, naming the first such field.
func (l DocumentLayout) validateRequested() error {
	if l.ContentHeight != ContentHeightContent && l.ContentHeight != ContentHeightViewport {
		return errors.New("content_height must be CONTENT or VIEWPORT")
	}
	if l.PageChrome != PlacementFlow && l.PageChrome != PlacementPinned {
		return errors.New("page_chrome must be FLOW or PINNED")
	}
	if l.Footer != PlacementFlow && l.Footer != PlacementPinned {
		return errors.New("footer must be FLOW or PINNED")
	}
	return nil
}

// validateStored rejects a stored layout a read cannot render.
func (l DocumentLayout) validateStored() error {
	switch {
	case l.ContentHeight != ContentHeightContent && l.ContentHeight != ContentHeightViewport:
		return fmt.Errorf("invalid persisted document layout: invalid contentHeight %q", l.ContentHeight)
	case l.PageChrome != PlacementFlow && l.PageChrome != PlacementPinned:
		return fmt.Errorf("invalid persisted document layout: invalid pageChrome %q", l.PageChrome)
	case l.Footer != PlacementFlow && l.Footer != PlacementPinned:
		return fmt.Errorf("invalid persisted document layout: invalid footer %q", l.Footer)
	}
	return nil
}

// Action is one thing a caller may do to a Post, as the manage API lists
// them.
type Action int

const (
	ActionViewVersions Action = iota + 1
	ActionEdit
	ActionDelete
	ActionAddAuthor
	ActionRemoveAuthor
	ActionManageCollaborators
	ActionManageShareLinks
	ActionModerateComments
	ActionRestoreVersion
	ActionPublishNow
	ActionSchedule
	ActionCancelSchedule
	ActionUnpublish
	ActionArchive
	ActionRepublish
)

// View is a Post as its editors read it.
type View struct {
	Post
	Document      *contentv1.RichTextDocument
	BlockMedia    []*contentv1.ContentBlockMediaItem
	OgAsset       *mediaasset.Reference
	Authors       []*memberref.Summary
	FeaturedImage *fileref.Delivery
	Actions       []Action
}

// Pagination is where a page sits among every match.
type Pagination struct {
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// PostPage is one page of Posts.
type PostPage struct {
	Posts      []View
	Pagination Pagination
}

// AdminView is a Post of the administrator list, with its comment count.
type AdminView struct {
	View
	CommentCount int32
}

// AdminPage is one page of the administrator Post list.
type AdminPage struct {
	Posts      []AdminView
	Pagination Pagination
}

// SettingsUpdate is the outcome of UpdatePost: the settings as stored.
type SettingsUpdate struct {
	ID                    string
	Changed               bool
	Slug                  *string
	CommentsEnabled       bool
	MapPlaceID            *string
	DocumentLayout        DocumentLayout
	ConfigurationRevision string
	UpdatedAt             time.Time
}

// Lifecycle is a Post's lifecycle after a publish, unpublish, archive,
// schedule, cancellation or republish.
type Lifecycle struct {
	ID                string
	Status            Status
	ScheduledAt       *time.Time
	ScheduledTimeZone *string
	PublishedAt       *time.Time
	UpdatedAt         time.Time
	Actions           []Action
}

// FeaturedImage is the outcome of setting a Post's featured image.
type FeaturedImage struct {
	Delivery *fileref.Delivery
	// OgRunID is the OG generation the change started, when it started one.
	OgRunID *string
}

// ParticipantRole is a Post participant's role.
type ParticipantRole int

const (
	RoleNone ParticipantRole = iota
	RoleAuthor
	RoleCollaborator
)

// Participant is one durable author or collaborator of a Post.
type Participant struct {
	Member                *memberref.Summary
	Role                  ParticipantRole
	CreatedAt             time.Time
	HasEffectiveAuthority bool
}

// Version is one recorded version of a Post's source content.
type Version struct {
	ID            string
	Version       int32
	SourceLocale  string
	Title         string
	Summary       *string
	CanonicalHash string
	CreatedAt     time.Time
	Contributors  []VersionContributor
}

// VersionContributor is a Member a version is attributed to.
type VersionContributor struct {
	MemberID string
	Nickname string
}

// VersionPage is one page of a Post's versions, newest first.
type VersionPage struct {
	Versions   []Version
	Pagination Pagination
}
