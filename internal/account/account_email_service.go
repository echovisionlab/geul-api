package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

// ErrMemberPrimaryEmailUnavailable means a credential mutation would remove
// the only usable Identity proof backing the canonical account email. Member
// primary_email is the deletion-safe projection of this value.
var ErrMemberPrimaryEmailUnavailable = errors.New("canonical account email is no longer backed by a usable identity candidate")

type accountEmailIdentityManager interface {
	auth.IdentityGetter
}

type AccountEmailService struct {
	db              *gorm.DB
	identityManager accountEmailIdentityManager
	memberEmails    MemberEmailProjection
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

func NewAccountEmailService(
	db *gorm.DB,
	identityManager accountEmailIdentityManager,
	memberEmails MemberEmailProjection,
) *AccountEmailService {
	return &AccountEmailService{db: db, identityManager: identityManager, memberEmails: memberEmails}
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
	if s == nil || s.db == nil {
		return fmt.Errorf("member database is required")
	}
	if s.memberEmails == nil {
		return fmt.Errorf("member email projection is required")
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
	primaryEmail, err := s.memberEmails.PrimaryEmail(ctx, s.db, identity.ExternalID, identityID)
	if err != nil {
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
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("member database is required")
	}
	if s.memberEmails == nil {
		return nil, fmt.Errorf("member email projection is required")
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.memberEmails.SyncEmailProjection(ctx, tx, identity.ExternalID, identityID, primaryEmail, emails)
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("identity %s has no exact active Member link: %w", identityID, err)
		}
		return nil, err
	}
	return rows, nil
}
