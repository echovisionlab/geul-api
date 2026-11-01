package sharelink

import (
	"context"
	"errors"
	"time"
)

// Link is a stored Share Link: a token that opens one target, optionally
// behind a password, until it expires.
type Link struct {
	ID         string
	Token      string
	EntityType EntityType
	EntityID   string
	Label      *string
	// PasswordHash is the Argon2id hash of the password a visitor presents
	// with the token, or nil when the token alone opens the target.
	PasswordHash *string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// Target is the stored state of a link target that decides what a link
// change requires of the caller.
type Target struct {
	// Archived reports a target in its archived lifecycle, whose links need
	// the archived actions.
	Archived bool
	// Scheduled reports a legal version scheduled to take effect at
	// EffectiveFrom.
	Scheduled     bool
	EffectiveFrom *time.Time
	// Version is a legal version's number, which its audit records.
	Version int64
}

var (
	// ErrLinkNotFound answers a link that does not exist, or a proof that
	// opens none.
	ErrLinkNotFound = errors.New("share link not found")
	// ErrTargetNotFound answers a link target that does not exist.
	ErrTargetNotFound = errors.New("share link target not found")
)

// Store is the share_link table, and the link targets other domains own,
// within one transaction or read snapshot.
type Store interface {
	// ListLinks returns the links of one target, newest first.
	ListLinks(ctx context.Context, entityType EntityType, entityID string) ([]Link, error)
	// Link returns the link of id, or ErrLinkNotFound.
	Link(ctx context.Context, id string) (Link, error)
	// LinkByToken returns the link of token, or ErrLinkNotFound.
	LinkByToken(ctx context.Context, token string) (Link, error)
	// LiveLink returns the link of token on the target that expires after
	// now, or ErrLinkNotFound.
	LiveLink(ctx context.Context, token string, entityType EntityType, entityID string, now time.Time) (Link, error)
	// InsertLink stores link and returns it as stored, with its ID.
	InsertLink(ctx context.Context, link Link) (Link, error)
	// InsertLinkUnlessTokenExists stores link unless a link already holds
	// its token, which it then leaves as it is.
	InsertLinkUnlessTokenExists(ctx context.Context, link Link) error
	// DeleteLink deletes link while it still names its target, or answers
	// ErrLinkNotFound.
	DeleteLink(ctx context.Context, link Link) error
	// DeleteTargetLinks deletes every link of entityID stored under one of
	// entityTypes.
	DeleteTargetLinks(ctx context.Context, entityID string, entityTypes ...EntityType) error
	// DeletePasswordFreeLink deletes the link of token on the target unless
	// it requires a password.
	DeletePasswordFreeLink(ctx context.Context, token string, entityType EntityType, entityID string) error
	// DeleteExpiredLinks deletes the links that expired before now and
	// returns how many it deleted.
	DeleteExpiredLinks(ctx context.Context, now time.Time) (int64, error)
	// Target reads the state of a link target, or answers ErrTargetNotFound.
	Target(ctx context.Context, entityType EntityType, entityID string) (Target, error)
	// LockTarget locks the target row for a link change and reads its
	// state, or answers ErrTargetNotFound.
	LockTarget(ctx context.Context, entityType EntityType, entityID string) (Target, error)
}
