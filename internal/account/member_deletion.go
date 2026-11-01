package account

import "time"

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
