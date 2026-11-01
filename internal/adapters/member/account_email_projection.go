package memberadapter

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// AccountEmailProjection is the Account email projection a Member
// registration and welcome email read: Identity reads go to Identity, and
// the projection syncs in its own transaction on DB.
type AccountEmailProjection struct {
	Units    account.Units
	Identity auth.IdentityGetter
}

var (
	_ memberdomain.RegistrationAccountEmailProjection = AccountEmailProjection{}
	_ memberpostgres.WelcomeRecipients                = AccountEmailProjection{}
)

func (p AccountEmailProjection) ResolveDelivery(
	ctx context.Context, tx *gorm.DB, identityID string,
) (string, string, string, error) {
	inTransaction := func(_ context.Context, work func(account.Store) error) error {
		return work(accountpostgres.Bind(tx, accountpostgres.Ports{MemberEmails: accountMemberEmailProjection{}}))
	}
	result, reason, err := account.ResolveMemberPrimaryEmailForIdentity(ctx, inTransaction, p.Identity, identityID)
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

func (p AccountEmailProjection) SyncMemberEmailProjection(
	ctx context.Context,
	identityID string,
	identity *auth.Identity,
) error {
	var candidates []account.AccountEmailProviderCandidate
	if identity != nil {
		candidates = account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	}
	_, err := account.NewAccountEmailService(p.Identity, p.Units).
		SyncMemberEmailProjection(ctx, identityID, identity, candidates)
	return err
}

// accountMemberEmailProjection is local to the inverse Member consumer
// adapter. Account-facing composition uses adapters/account instead.
type accountMemberEmailProjection struct{}

func (accountMemberEmailProjection) PrimaryEmail(
	ctx context.Context, db *gorm.DB, memberID, identityID string,
) (string, error) {
	email, err := memberdomain.PrimaryEmail(ctx, memberpostgres.Bind(db), memberID, identityID)
	if errors.Is(err, memberdomain.ErrMemberNotFound) {
		return "", gorm.ErrRecordNotFound
	}
	return email, err
}

func (accountMemberEmailProjection) SyncEmailProjection(
	ctx context.Context, db *gorm.DB, memberID, identityID, primaryEmail string, availableEmails []string,
) error {
	err := memberdomain.SyncEmailProjection(ctx, memberpostgres.Bind(db), memberID, identityID, primaryEmail, availableEmails)
	if errors.Is(err, memberdomain.ErrMemberNotFound) {
		return gorm.ErrRecordNotFound
	}
	return err
}
