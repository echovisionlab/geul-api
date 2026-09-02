package member

import (
	"context"
	"fmt"
	"time"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
)

// DeletionLifecycle is the Member implementation of Account's narrow
// deletion consumer boundary. It owns Member resource authorization, profile
// state, local references, and avatar asset lifecycle.
type DeletionLifecycle struct{}

// DeletionAudit is supplied by the Account owner and is invoked only for an
// actual terminal Member transition, inside the transaction that owns it.
type DeletionAudit func(context.Context, *gorm.DB, string) error

// DeletionSnapshot is Member's verified state before Account terminates the
// matching Kratos identity.
type DeletionSnapshot struct {
	MemberID             string
	IdentityID           string
	PrimaryEmailSnapshot string
	AlreadyTombstoned    bool
	IdentityLinked       bool
}

// UnonboardedDeletionTarget is the Member-owned hard-delete target. A zero
// target means the command is stale or already complete.
type UnonboardedDeletionTarget struct {
	MemberID       string
	IdentityID     string
	IdentityLinked bool
}

// DeletionNotification is the Member profile projection captured in an
// Account-owned deletion request.
type DeletionNotification struct {
	Nickname     string
	PrimaryEmail string
	Locale       *string
}

// UnonboardedCandidate is a Member-owned retention predicate result.
type UnonboardedCandidate struct {
	MemberID   string
	IdentityID string
	CreatedAt  time.Time
}

// UnonboardedCursor carries the stable bounded-page ordering for retention.
type UnonboardedCursor struct {
	CreatedAt time.Time
	MemberID  string
}

func memberEmailsEqual(left, right string) bool {
	return emailutil.NormalizeAddressForDelivery(left) == emailutil.NormalizeAddressForDelivery(right)
}

func validateDeletionPair(memberID, identityID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	if memberID == identityID {
		return fmt.Errorf("member_id and identity_id must be distinct")
	}
	return nil
}
