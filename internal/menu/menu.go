// Package menu owns Menu structure and locale-label projection rules.
package menu

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/google/uuid"
)

// LinkType is the kind of target a Menu item links to, as stored.
type LinkType string

// The stored link types. A request may carry another value, which validation
// rejects; the empty value means the request left it unspecified.
const (
	LinkCustom   LinkType = "custom"
	LinkPage     LinkType = "page"
	LinkCategory LinkType = "category"
	LinkTag      LinkType = "tag"
	LinkSeries   LinkType = "series"
)

// VisibilityMode says who sees a Menu item, as stored.
type VisibilityMode string

// The stored visibility modes. A request may leave the mode empty, which is
// stored as VisibilityAll.
const (
	VisibilityAll           VisibilityMode = "all"
	VisibilityAuthenticated VisibilityMode = "authenticated"
	VisibilityGuest         VisibilityMode = "guest"
	VisibilityRoles         VisibilityMode = "roles"
)

// The stored localization modes of a Menu item label.
const (
	LocalizationTranslated  = "translated"
	LocalizationFixedLocale = "fixed_locale"
)

// Item is one node of a Menu item tree. Its JSON form is the Menu's stored
// item document, which translation and the public Site Settings projection
// read too.
type Item struct {
	ID               string      `json:"id"`
	Label            string      `json:"label"`
	LinkType         LinkType    `json:"linkType"`
	URL              *string     `json:"url,omitempty"`
	TargetID         *string     `json:"targetId,omitempty"`
	TargetSlug       *string     `json:"targetSlug,omitempty"`
	OpenInNewTab     *bool       `json:"openInNewTab,omitempty"`
	Visibility       *Visibility `json:"visibility,omitempty"`
	LocalizationMode *string     `json:"localizationMode,omitempty"`
	FixedLocale      *string     `json:"fixedLocale,omitempty"`
	Children         []Item      `json:"children,omitempty"`
}

// Visibility limits who sees a Menu item.
type Visibility struct {
	Mode  VisibilityMode `json:"mode"`
	Roles []string       `json:"roles,omitempty"`
}

// Root is one stored Menu root. ItemsJSON stays encoded because each use
// case answers a stored tree that no longer decodes in its own way: a list
// omits that Menu, a target rewrite fails, an update requires Manage.
type Root struct {
	ID                string
	ContentDocumentID string
	SourceLocale      string
	Name              string
	ItemsJSON         []byte
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// RootChange is the part of a Root an update writes. A nil Name or ItemsJSON
// leaves that column alone.
type RootChange struct {
	Name      *string
	ItemsJSON []byte
	UpdatedAt time.Time
}

// Menu is a Menu with its decoded item tree, as the use cases return it.
type Menu struct {
	ID           string
	SourceLocale string
	Name         string
	Items        []Item
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// MenuPage is one page of Menus ordered by name.
type MenuPage struct {
	Menus   []Menu
	Total   int32
	Limit   int32
	Offset  int32
	HasMore bool
}

// ContentDocument is the Content Document a Menu root owns, as stored.
type ContentDocument struct {
	ID       uuid.UUID
	Profile  string
	Revision string
}

// LocaleValues are the stored values-only labels of one Menu locale, keyed
// by item ID. A missing key is a missing label; a present empty string is an
// explicit empty label.
type LocaleValues struct {
	MenuID    string
	Locale    string
	Labels    map[string]string
	UpdatedAt time.Time
}

// TargetReference identifies an owning-domain resource selected by a Menu
// item. Menu owns the link policy; the Store adapter owns the lookup.
type TargetReference struct {
	LinkType LinkType
	ID       string
	Slug     string
}

// TargetNotFoundError is LockTargets' answer when an item target of
// LinkType does not exist.
type TargetNotFoundError struct {
	LinkType LinkType
}

func (err TargetNotFoundError) Error() string {
	return "menu " + string(err.LinkType) + " target not found"
}

// ContributorPermission is what a collaboration save needs its contributors
// to hold.
type ContributorPermission uint8

// Edit saves labels and fields; Manage also changes the item topology.
const (
	ContributorEdit ContributorPermission = iota + 1
	ContributorManage
)

// AIDocumentChange is the content.updated signal of one AI document
// mutation, scheduled in its transaction.
type AIDocumentChange struct {
	MenuID           string
	Locale           string
	DocumentRevision string
	TargetRevision   *string
	Changed          bool
	Deleted          bool
	MemberID         string
}

// The outcomes the use cases branch on.
var (
	ErrMenuNotFound            = errors.New("menu not found")
	ErrMenuNameTaken           = errors.New("menu name is taken")
	ErrContentDocumentNotFound = errors.New("menu content document not found")
	ErrRevisionChanged         = errors.New("menu content document revision changed")
	ErrLocaleNotFound          = errors.New("menu locale values not found")
)

// Store is the Menu persistence one transaction or read snapshot sees.
// menupostgres implements it; menutest keeps it in memory.
type Store interface {
	// Menu reads one root; ErrMenuNotFound when it does not exist.
	Menu(ctx context.Context, id string) (Root, error)
	// LockMenu reads one root and locks it for update.
	LockMenu(ctx context.Context, id string) (Root, error)
	// LockMenus locks every root for update, ordered by ID.
	LockMenus(ctx context.Context) ([]Root, error)
	MenuExists(ctx context.Context, id string) (bool, error)
	// ListMenus returns one page of roots ordered by name and the total.
	ListMenus(ctx context.Context, limit, offset int) ([]Root, int64, error)
	// MenuNameTaken reports whether another root than exceptID (any root
	// when it is empty) has name.
	MenuNameTaken(ctx context.Context, name, exceptID string) (bool, error)
	// InsertMenu stores root under a new ID and returns it as stored;
	// ErrMenuNameTaken when the name is taken.
	InsertMenu(ctx context.Context, root Root) (Root, error)
	// UpdateMenu writes change; ErrMenuNotFound when no root has id,
	// ErrMenuNameTaken when the new name is taken.
	UpdateMenu(ctx context.Context, id string, change RootChange) error
	// DeleteMenu deletes the root and, with it, its locale values.
	DeleteMenu(ctx context.Context, id string) error

	// ContentDocument reads a Content Document; ErrContentDocumentNotFound
	// when it does not exist.
	ContentDocument(ctx context.Context, id uuid.UUID) (ContentDocument, error)
	LockContentDocument(ctx context.Context, id uuid.UUID) (ContentDocument, error)
	InsertContentDocument(ctx context.Context, id uuid.UUID, profile string, revision uuid.UUID, at time.Time) error
	// AdvanceContentDocument moves the document menuID owns from expected
	// to next; ErrRevisionChanged when it is not at expected or menuID does
	// not own it.
	AdvanceContentDocument(ctx context.Context, menuID string, id, expected, next uuid.UUID, at time.Time) error
	DeleteContentDocument(ctx context.Context, id uuid.UUID) error

	// Locale reads the labels of one locale; ErrLocaleNotFound when the
	// locale has no row.
	Locale(ctx context.Context, menuID, locale string) (LocaleValues, error)
	LockLocale(ctx context.Context, menuID, locale string) (LocaleValues, error)
	// Locales reads every locale of a Menu.
	Locales(ctx context.Context, menuID string) ([]LocaleValues, error)
	// InsertLocale creates a locale row at values.UpdatedAt.
	InsertLocale(ctx context.Context, values LocaleValues) error
	// UpdateLocale rewrites a locale row; ErrLocaleNotFound when it has none.
	UpdateLocale(ctx context.Context, values LocaleValues) error
	// UpsertLocale creates or rewrites a locale row.
	UpsertLocale(ctx context.Context, values LocaleValues) error
	// DeleteLocale deletes a locale row; ErrLocaleNotFound when it has none.
	DeleteLocale(ctx context.Context, menuID, locale string) error

	// LockTargets share-locks the item targets; a TargetNotFoundError when
	// one does not exist.
	LockTargets(ctx context.Context, targets []TargetReference) error
	// ClearSiteSettingsMenu unbinds the Site Settings slots that select the
	// Menu, locking the Site Settings singleton first.
	ClearSiteSettingsMenu(ctx context.Context, menuID string) error
	// RequireContributors checks that the contributors of a collaboration
	// save are active and hold permission on the Menu.
	RequireContributors(ctx context.Context, menuID string, contributors []string, permission ContributorPermission) error
	// PublishAIDocumentChange schedules the content.updated signal of an AI
	// document mutation for an interactive caller.
	PublishAIDocumentChange(ctx context.Context, change AIDocumentChange) error
}

// Joined is a transaction a pipeline outside Menu opened, which a Menu step
// joins instead of opening its own: a translation apply, or an owning
// domain's target rename. Store and Audit are both bound to that
// transaction, so the step passes a nil transaction to domainaudit.
// menuadapter.Join binds a gorm transaction; a test joins the fake Store
// and an audittest.Recorder.
type Joined struct {
	Store Store
	Audit domainaudit.Appender
}
