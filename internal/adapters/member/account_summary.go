package memberadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
)

// Accounts reads the Account projections a Member view shows, outside the
// Member store's snapshot.
type Accounts struct {
	Accounts *account.Service
}

var _ memberdomain.Accounts = Accounts{}

func (a Accounts) SessionSummary(ctx context.Context, memberID string) (*memberdomain.AccountSummary, error) {
	return accountSummary(a.Accounts.SessionSummary(ctx, memberID))
}

func (a Accounts) Summary(ctx context.Context, memberID string) (*memberdomain.AccountSummary, error) {
	return accountSummary(a.Accounts.Summary(ctx, memberID))
}

func (a Accounts) Summaries(ctx context.Context, memberIDs []string) (map[string]*memberdomain.AccountSummary, error) {
	summaries, err := a.Accounts.Summaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*memberdomain.AccountSummary, len(summaries))
	for memberID, summary := range summaries {
		result[memberID], _ = accountSummary(summary, nil)
	}
	return result, nil
}

func (Accounts) AdminDetails(ctx context.Context, identity *auth.Identity) (*memberdomain.AccountDetails, error) {
	candidates := account.ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	rows := account.ProjectedAccountEmailRows(identity, candidates)
	details := &memberdomain.AccountDetails{}
	for _, provider := range account.Providers(identity) {
		details.Providers = append(details.Providers, memberdomain.AccountProvider{Provider: provider.Provider, Identifier: provider.Identifier})
	}
	for _, row := range rows {
		candidate := memberdomain.EmailCandidate{
			Email:             row.DisplayEmail,
			NormalizedEmail:   row.NormalizedEmail,
			Current:           row.IsCurrentEmail,
			IdentityVerified:  row.IdentityVerified,
			EffectiveTrusted:  row.EffectiveTrusted,
			UsableForDelivery: row.UsableForDelivery,
		}
		for _, source := range row.Sources {
			candidate.Sources = append(candidate.Sources, memberdomain.EmailSource{
				Type: memberdomain.EmailSourceType(source.Type), Provider: source.Provider, ProviderSubject: source.ProviderSubject,
			})
		}
		details.EmailCandidates = append(details.EmailCandidates, candidate)
	}
	return details, nil
}

func accountSummary(summary *account.Summary, err error) (*memberdomain.AccountSummary, error) {
	if err != nil || summary == nil {
		return nil, err
	}
	result := &memberdomain.AccountSummary{MemberID: summary.MemberID, Role: summary.Role, Status: memberdomain.AdminStatus(summary.Status), Banned: summary.Banned}
	if email := summary.CanonicalEmail; email != nil {
		result.CanonicalEmail = &memberdomain.CanonicalEmail{Email: email.Email, Verified: email.Verified}
	}
	if ban := summary.BanDetails; ban != nil {
		result.BanDetails = &memberdomain.BanDetails{MetadataBanned: ban.MetadataBanned, IdentityState: ban.IdentityState, InactiveState: ban.InactiveState, Reason: ban.Reason, ExpiresAt: ban.ExpiresAt}
	}
	return result, nil
}
