package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/mutation"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

// ErrMemberPrimaryEmailUnavailable means a credential mutation would remove
// the only usable Identity proof backing the canonical account email. Member
// primary_email is the deletion-safe projection of this value.
var ErrMemberPrimaryEmailUnavailable = errors.New("canonical account email is no longer backed by a usable identity candidate")

// StepFunc runs work in one transaction: a step of a fenced flow, or a
// transaction of its own.
type StepFunc func(ctx context.Context, work func(Store) error) error

// AccountEmailService derives the proven email candidates of an Identity and
// keeps the Member email projection in step with them.
type AccountEmailService struct {
	identityManager auth.IdentityGetter
	step            StepFunc
}

func newAccountEmailService(identityManager auth.IdentityGetter, step StepFunc) *AccountEmailService {
	return &AccountEmailService{identityManager: identityManager, step: step}
}

// NewAccountEmailService returns the email projection of identities over
// units, each sync in a transaction of its own.
func NewAccountEmailService(identities auth.IdentityGetter, units Units) *AccountEmailService {
	return newAccountEmailService(identities, func(ctx context.Context, work func(Store) error) error {
		return units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error { return work(tx.Store()) })
	})
}

type AccountEmailProviderCandidate struct {
	Provider        string
	ProviderSubject string
	Email           string
	Verified        bool
}

type AccountEmailBackfillResult struct {
	Processed int
	Synced    int
	Failed    int
}

// EnsureMemberPrimaryEmailUsable rejects an Identity credential projection
// that would leave the canonical account email without proof. Member primary
// is a deletion-safe projection of that canonical value, never an independent
// user selection.
func (s *AccountEmailService) EnsureMemberPrimaryEmailUsable(
	ctx context.Context,
	identityID string,
	identity *auth.Identity,
	providerCandidates []AccountEmailProviderCandidate,
) error {
	if s == nil || s.step == nil {
		return fmt.Errorf("member database is required")
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return fmt.Errorf("identity id is required")
	}
	if identity == nil {
		return fmt.Errorf("identity is required")
	}
	if strings.TrimSpace(identity.ExternalID) == "" {
		return fmt.Errorf("identity external id is required")
	}
	var primaryEmail string
	if err := s.step(ctx, func(store Store) error {
		var err error
		primaryEmail, err = store.MemberPrimaryEmail(ctx, identity.ExternalID, identityID)
		return err
	}); err != nil {
		return err
	}
	canonical := emailutil.NormalizeAddressForDelivery(identity.CurrentEmail())
	if emailutil.NormalizeAddressForDelivery(primaryEmail) != canonical {
		return ErrMemberPrimaryEmailUnavailable
	}
	row := findProjectionRow(projectedAccountEmailRows(identity, providerCandidates), canonical)
	if row == nil || !row.UsableForDelivery {
		return ErrMemberPrimaryEmailUnavailable
	}
	return nil
}

// SyncMemberEmailProjection replaces the active Member email projection with
// the exact proven usable candidates derived from Kratos. primary_email always
// follows the verified canonical Identity email.
func (s *AccountEmailService) SyncMemberEmailProjection(
	ctx context.Context,
	identityID string,
	identity *auth.Identity,
	providerCandidates []AccountEmailProviderCandidate,
) ([]AccountEmailProjection, error) {
	if s == nil || s.step == nil {
		return nil, fmt.Errorf("member database is required")
	}
	identityID = strings.TrimSpace(identityID)
	if identityID == "" {
		return nil, fmt.Errorf("identity id is required")
	}
	if identity == nil {
		if s.identityManager == nil {
			return nil, fmt.Errorf("identity manager is required to load email projection")
		}
		var err error
		identity, err = LoadIdentityWithEmailCredentials(ctx, s.identityManager, identityID)
		if err != nil {
			return nil, err
		}
	}
	if identity == nil || identity.ID != identityID {
		return nil, fmt.Errorf("exact identity %s is required for Member email sync", identityID)
	}
	if strings.TrimSpace(identity.ExternalID) == "" {
		return nil, fmt.Errorf("identity external id is required")
	}
	rows := projectedAccountEmailRows(identity, providerCandidates)
	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.UsableForDelivery {
			emails = append(emails, row.NormalizedEmail)
		}
	}
	sort.Strings(emails)
	if len(emails) == 0 {
		return nil, fmt.Errorf("identity %s has no proven usable email candidate", identityID)
	}

	primaryEmail := emailutil.NormalizeAddressForDelivery(identity.CurrentEmail())
	if !slices.Contains(emails, primaryEmail) {
		return nil, fmt.Errorf("identity %s canonical email is not a proven usable candidate", identityID)
	}
	err := s.step(ctx, func(store Store) error {
		return store.SyncMemberEmails(ctx, identity.ExternalID, identityID, primaryEmail, emails)
	})
	if err != nil {
		if errors.Is(err, ErrMemberLinkNotFound) {
			return nil, fmt.Errorf("identity %s has no exact active Member link: %w", identityID, err)
		}
		return nil, err
	}
	return rows, nil
}
