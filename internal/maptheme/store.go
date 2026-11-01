package maptheme

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Theme is a stored Map Theme: its snapshot at Revision, which every saved
// change bumps. Settings.CalloutScale is the stored float32 scale read back
// as float64.
type Theme struct {
	ID        string
	Revision  int64
	Snapshot  Snapshot
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ResolvedTheme is one color scheme of a theme.
type ResolvedTheme struct {
	ThemeID  string
	Scheme   string
	Settings Settings
	Variant  Variant
}

// Resolve is the variant of theme in scheme, which is "light" or "dark".
func (theme Theme) Resolve(scheme string) ResolvedTheme {
	variant := theme.Snapshot.LightVariant
	if scheme == "dark" {
		variant = theme.Snapshot.DarkVariant
	}
	return ResolvedTheme{ThemeID: theme.ID, Scheme: scheme, Settings: theme.Snapshot.Settings, Variant: variant}
}

var (
	// ErrThemeNotFound answers a read, lock or delete of a theme that does
	// not exist.
	ErrThemeNotFound = errors.New("map theme not found")
	// ErrRevisionChanged answers a snapshot update whose expected revision is
	// no longer the stored one, or whose theme is gone.
	ErrRevisionChanged = errors.New("map theme revision changed")
	// ErrDefaultTheme answers a delete of the theme the site settings name
	// as the default.
	ErrDefaultTheme = errors.New("map theme is the site default")
)

// Store is what the Map Theme use cases read and write, bound to one
// transaction or read snapshot.
type Store interface {
	// DefaultThemeID reads the site default theme.
	DefaultThemeID(ctx context.Context) (string, error)
	// LockDefaultThemeID locks the site settings for update and reads the
	// default theme.
	LockDefaultThemeID(ctx context.Context) (string, error)
	// SetDefaultThemeID makes themeID the site default.
	SetDefaultThemeID(ctx context.Context, themeID string) error
	// Themes lists every theme by name, then ID.
	Themes(ctx context.Context) ([]Theme, error)
	// ThemesByID reads those of ids that exist, in no particular order.
	ThemesByID(ctx context.Context, ids []string) ([]Theme, error)
	// Theme reads one theme, or answers ErrThemeNotFound.
	Theme(ctx context.Context, id string) (Theme, error)
	// LockTheme locks one theme for update and reads it, or answers
	// ErrThemeNotFound.
	LockTheme(ctx context.Context, id string) (Theme, error)
	// InsertTheme stores snapshot as a new theme at revision 1.
	InsertTheme(ctx context.Context, snapshot Snapshot) (Theme, error)
	// UpdateSnapshot replaces the snapshot of theme id at expectedRevision
	// and bumps its revision, or answers ErrRevisionChanged.
	UpdateSnapshot(ctx context.Context, id string, expectedRevision int64, snapshot Snapshot) error
	// DeleteTheme deletes theme id, or answers ErrThemeNotFound, or
	// ErrDefaultTheme while it is the site default.
	DeleteTheme(ctx context.Context, id string) error
	// LockContributor re-resolves memberID to its live, linked Identity and
	// locks both (authorizationtarget.RequireLocked). Its errors keep that
	// helper's classification: authorizationtarget.FailedToDecide tells a
	// failure from a Member that cannot contribute.
	LockContributor(ctx context.Context, memberID string) (identityID string, err error)
}

// Authorizer is the SpiceDB port of the Map Theme use cases: the pipeline's
// checks and relationship writes, and the capability check of a
// collaboration contributor.
type Authorizer interface {
	mutation.Authorizer
	CheckActorCan(ctx context.Context, actor policyv1.Actor, can policyv1.Can) (bool, error)
}
