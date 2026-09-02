package account

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"gorm.io/gorm"
)

// MemberDeletionLifecycle is the Account-owned consumer boundary for the
// Member aggregate portions of account deletion. Account owns the durable
// command, Kratos, account-identity subject cleanup, and anchor; Member owns
// its resource relationships, profile lifecycle, and avatar binding state.
type MemberDeletionLifecycle interface {
	PrepareTombstone(context.Context, *gorm.DB, string, string, string) (MemberDeletionSnapshot, error)
	FinalizeTombstone(context.Context, *gorm.DB, MemberDeletionSnapshot, time.Time, MemberDeletionAudit) error
	PrepareUnonboardedHardDelete(context.Context, *gorm.DB, string, string) (MemberUnonboardedDeletionTarget, error)
	FinalizeUnonboardedHardDelete(context.Context, *gorm.DB, *auth.SpiceDBClient, MemberUnonboardedDeletionTarget, MemberDeletionAudit) error
	CleanupAvatar(context.Context, *gorm.DB, string, string) error
	NotificationSnapshot(context.Context, *gorm.DB, string, string) (MemberDeletionNotification, error)
	CompletionEligible(context.Context, *gorm.DB, string) (bool, error)
	AvatarAssetID(context.Context, *gorm.DB, string) (string, error)
	ListExpiredUnonboarded(context.Context, *gorm.DB, time.Time, int, *MemberUnonboardedCursor) ([]MemberUnonboardedCandidate, *MemberUnonboardedCursor, error)
	RecheckUnonboarded(context.Context, *gorm.DB, string, string, *time.Time) (bool, error)
	UnonboardedIdentity(context.Context, *gorm.DB, string) (string, bool, error)
}

// MemberDeletionAudit is Account's owning callback for the terminal
// account.deleted audit. Member invokes it inside the same transaction as the
// actual tombstone or hard-delete transition, so an audit failure rolls back
// the Member state change without making Member own an Account audit action.
type MemberDeletionAudit func(context.Context, *gorm.DB, string) error

// MemberDeletionSnapshot is the verified Member state needed by the Account
// command before it terminates the matching Kratos identity.
type MemberDeletionSnapshot struct {
	MemberID             string
	IdentityID           string
	PrimaryEmailSnapshot string
	AlreadyTombstoned    bool
	IdentityLinked       bool
}

// MemberUnonboardedDeletionTarget is the Member-owned snapshot accepted for
// a hard-delete command. A zero target denotes a stale or duplicate command.
type MemberUnonboardedDeletionTarget struct {
	MemberID       string
	IdentityID     string
	IdentityLinked bool
}

// MemberDeletionNotification is the Member-owned profile projection captured
// in Account's durable deletion request before the identity is disabled.
type MemberDeletionNotification struct {
	Nickname     string
	PrimaryEmail string
	Locale       *string
}

// MemberUnonboardedCandidate is a read-only Member-owned retention predicate
// result. Account uses it solely to hold the matching identity fence and
// enqueue its durable lifecycle command.
type MemberUnonboardedCandidate struct {
	MemberID   string
	IdentityID string
	CreatedAt  time.Time
}

// MemberUnonboardedCursor preserves the Member-owned created-at/id ordering
// between bounded scheduler pages.
type MemberUnonboardedCursor struct {
	CreatedAt time.Time
	MemberID  string
}
