package member

import (
	"context"
	"fmt"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

// PrimaryEmail returns the exact linked Member delivery-primary projection.
// The Identity reverse pointer is part of every read so a stale or malformed
// pair is indistinguishable from a missing Member projection
// (ErrMemberNotFound).
func PrimaryEmail(ctx context.Context, store EmailProjectionStore, memberID, identityID string) (string, error) {
	if !memberEmailProjectionPair(memberID, identityID) {
		return "", ErrMemberNotFound
	}
	email, err := store.PrimaryEmail(ctx, memberID, identityID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(email) == "" {
		return "", ErrMemberNotFound
	}
	return strings.TrimSpace(email), nil
}

// SyncEmailProjection locks and replaces the exact linked Member email
// projection. Candidate proof and canonical-address selection remain Account
// responsibilities; this operation owns only Member state. Inputs must
// already be canonicalized delivery addresses, and Member still fails closed
// unless primary_email is present in available_emails.
func SyncEmailProjection(
	ctx context.Context,
	store EmailProjectionStore,
	memberID, identityID, primaryEmail string,
	availableEmails []string,
) error {
	if !memberEmailProjectionPair(memberID, identityID) {
		return ErrMemberNotFound
	}
	primaryEmail = emailutil.NormalizeAddressForDelivery(primaryEmail)
	if primaryEmail == "" {
		return fmt.Errorf("member primary email is required")
	}
	primaryFound := false
	for _, candidate := range availableEmails {
		if emailutil.NormalizeAddressForDelivery(candidate) != candidate {
			return fmt.Errorf("member available email must be canonicalized")
		}
		if candidate == primaryEmail {
			primaryFound = true
		}
	}
	if !primaryFound {
		return fmt.Errorf("member primary email must be an available email")
	}
	locked, err := store.LockLinkedMember(ctx, memberID, identityID)
	if err != nil {
		return err
	}
	if locked != memberID {
		return ErrMemberNotFound
	}
	return store.UpdateEmailProjection(ctx, memberID, identityID, primaryEmail, availableEmails)
}

func memberEmailProjectionPair(memberID, identityID string) bool {
	memberID = strings.TrimSpace(memberID)
	identityID = strings.TrimSpace(identityID)
	return memberID != "" && identityID != "" && memberID != identityID
}
