package oauthapp

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
)

// Record is the stored sign-in app: who may sign in through it, and the
// revision every write changes.
type Record struct {
	ClientID          string
	Access            Access
	MemberCriteriaIDs []string
	Revision          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ErrAppNotFound reports a client ID that is not a sign-in app.
var ErrAppNotFound = errors.New("sign-in app not found")

// ErrAppExists reports a client ID already used by a sign-in app.
var ErrAppExists = errors.New("sign-in app already exists")

// Store is the sign-in app state bound to one transaction
// (oauthapppostgres.NewUnits; oauthapptest.Store in unit tests).
type Store interface {
	Apps(ctx context.Context) ([]Record, error)
	App(ctx context.Context, clientID string) (Record, error)
	LockApp(ctx context.Context, clientID string) (Record, error)
	// InsertApp stores a new app with a fresh revision; ErrAppExists when
	// the client ID is taken.
	InsertApp(ctx context.Context, record Record) (Record, error)
	// UpdateApp replaces the app's access and Member criteria and gives it a
	// fresh revision, also when only its Hydra settings changed.
	UpdateApp(ctx context.Context, record Record) (Record, error)
	DeleteApp(ctx context.Context, clientID string) error
}

// Client is an OAuth client as the Hydra Admin API stores it.
type Client struct {
	ID                     string
	Name                   string
	RedirectURIs           []string
	PostLogoutRedirectURIs []string
	GrantTypes             []string
	ResponseTypes          []string
	Scope                  string
	// Audience names the resource tokens are issued for; connected AI tools
	// carry the MCP resource, sign-in apps none.
	Audience            []string
	AuthMethod          string
	AccessTokenLifespan time.Duration
	IDTokenLifespan     time.Duration
	// Secret is set only to write a new client secret; Hydra never returns
	// a stored one.
	Secret string
	// Kind is the metadata marker of who manages the client.
	Kind      string
	CreatedAt *time.Time
}

// signInAppKind marks a Hydra client this domain manages.
const signInAppKind = "sign_in_app"

// ErrClientNotFound reports a client ID Hydra does not know.
var ErrClientNotFound = errors.New("OAuth client not found")

// ErrClientExists reports a client ID Hydra already uses.
var ErrClientExists = errors.New("OAuth client already exists")

// Clients is the Hydra Admin API (internal/adapters/hydra).
type Clients interface {
	List(ctx context.Context) ([]Client, error)
	Get(ctx context.Context, clientID string) (Client, error)
	Create(ctx context.Context, client Client) error
	// Replace overwrites the client; an empty Secret keeps the stored one.
	Replace(ctx context.Context, client Client) error
	Delete(ctx context.Context, clientID string) error
}

// Eligibility reads who a sign-in app admits (internal/adapters/membercriteria).
type Eligibility interface {
	// CriteriaSummaries returns the Member criteria among ids that exist.
	CriteriaSummaries(ctx context.Context, ids []string) ([]CriteriaSummary, error)
	// ActiveMember reports whether user is a live Member who is not banned.
	ActiveMember(ctx context.Context, user *auth.UserInfo) (bool, error)
	// MatchesAny reports whether the Member user is selected by any of the
	// active Member criteria ids, by the rule restricted downloads use.
	MatchesAny(ctx context.Context, user *auth.UserInfo, ids []string) (bool, error)
}
