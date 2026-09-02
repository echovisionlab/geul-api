package memberadapter

import (
	"context"
	"fmt"
	"sort"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type AccountEmailProjection struct{}

var _ memberdomain.AccountEmailProjection = AccountEmailProjection{}

func (AccountEmailProjection) AdminDetails(
	ctx context.Context, identity *auth.Identity,
) (*managev1.AccountAdminDetails, error) {
	candidates := account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	rows := account.ProjectedAccountEmailRows(identity, candidates)
	details := &managev1.AccountAdminDetails{Providers: account.ProviderProto(identity)}
	for _, row := range rows {
		candidate := &managev1.AccountEmailCandidate{
			Email:             row.DisplayEmail,
			NormalizedEmail:   row.NormalizedEmail,
			Current:           row.IsCurrentEmail,
			IdentityVerified:  row.IdentityVerified,
			EffectiveTrusted:  row.EffectiveTrusted,
			UsableForDelivery: row.UsableForDelivery,
		}
		for _, source := range row.Sources {
			candidate.Sources = append(candidate.Sources, &managev1.AccountEmailSource{
				SourceType:      account.SourceTypeProto(string(source.SourceType)),
				Provider:        source.Provider,
				ProviderSubject: source.ProviderSubject,
			})
		}
		details.EmailCandidates = append(details.EmailCandidates, candidate)
	}
	return details, nil
}

func (AccountEmailProjection) ResolveDelivery(
	ctx context.Context, db *gorm.DB, identity auth.IdentityGetter, identityID string,
) (string, string, string, error) {
	result, reason, err := account.ResolveMemberPrimaryEmailForIdentity(ctx, db, accountMemberEmailProjection{}, identity, identityID)
	if err != nil || result == nil || result.Identity == nil {
		return "", "", reason, err
	}
	return result.Email, result.Identity.ExternalID, reason, nil
}

func (AccountEmailProjection) PrepareRegistration(
	ctx context.Context,
	identityManager auth.IdentityGetter,
	identityID string,
	requestedEmail string,
) (*auth.Identity, string, []string, error) {
	identity, err := account.LoadIdentityWithEmailCredentials(ctx, identityManager, identityID)
	if err != nil || identity == nil {
		if err == nil {
			err = fmt.Errorf("identity was not returned")
		}
		return nil, "", nil, err
	}
	if identity.ID != identityID {
		return nil, "", nil, fmt.Errorf("identity lookup returned a different identity")
	}
	if _, err := uuidutil.ParseCanonical(identity.ID, "identity_id"); err != nil {
		return nil, "", nil, err
	}
	if identity.State != auth.KratosStateActive || identity.IsBanned() {
		return nil, "", nil, fmt.Errorf("registration identity is not active")
	}
	normalized, ok := account.NormalizeAccountEmailInput(identity.CurrentEmail())
	if !ok || normalized != emailutil.NormalizeAddressForDelivery(requestedEmail) {
		return nil, "", nil, fmt.Errorf("registration email does not match the exact identity")
	}
	rows := account.ProjectedAccountEmailRows(
		identity,
		account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials),
	)
	current := account.FindAccountEmailProjection(rows, normalized)
	if current == nil || !current.UsableForDelivery {
		return nil, "", nil, fmt.Errorf("registration email is not proven by the exact identity")
	}
	if identity.ExternalID != "" {
		if _, err := uuidutil.ParseCanonical(identity.ExternalID, "identity.external_id"); err != nil {
			return nil, "", nil, err
		}
		if identity.ExternalID == identityID {
			return nil, "", nil, fmt.Errorf("identity_id and member_id must be distinct")
		}
	}
	emails := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.UsableForDelivery {
			emails = append(emails, row.NormalizedEmail)
		}
	}
	sort.Strings(emails)
	return identity, current.NormalizedEmail, emails, nil
}

func (AccountEmailProjection) SyncMemberEmailProjection(
	ctx context.Context,
	db *gorm.DB,
	identityManager auth.IdentityGetter,
	identityID string,
	identity *auth.Identity,
) error {
	var candidates []account.AccountEmailProviderCandidate
	if identity != nil {
		candidates = account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	}
	_, err := account.NewAccountEmailService(db, identityManager, accountMemberEmailProjection{}).
		SyncMemberEmailProjection(ctx, identityID, identity, candidates)
	return err
}

// accountMemberEmailProjection is local to the inverse Member consumer
// adapter. Account-facing composition uses adapters/account instead.
type accountMemberEmailProjection struct{}

func (accountMemberEmailProjection) PrimaryEmail(
	ctx context.Context, db *gorm.DB, memberID, identityID string,
) (string, error) {
	return memberdomain.PrimaryEmail(ctx, db, memberID, identityID)
}

func (accountMemberEmailProjection) SyncEmailProjection(
	ctx context.Context, db *gorm.DB, memberID, identityID, primaryEmail string, availableEmails []string,
) error {
	return memberdomain.SyncEmailProjection(ctx, db, memberID, identityID, primaryEmail, availableEmails)
}
