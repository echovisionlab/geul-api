package account

import (
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
)

type accountEmailProjectionDraft struct {
	displayEmail     string
	isCurrentEmail   bool
	identityVerified bool
	emailCodeUsable  bool
	oidcVerified     bool
	providerSources  []model.AccountEmailSource
}

type AccountEmailProjection struct {
	DisplayEmail      string
	NormalizedEmail   string
	IsCurrentEmail    bool
	IdentityVerified  bool
	EffectiveTrusted  bool
	UsableForDelivery bool
	Sources           []model.AccountEmailSource
}

func buildAccountEmailProjectionDrafts(
	identity *auth.Identity,
	providerCandidates []AccountEmailProviderCandidate,
) map[string]*accountEmailProjectionDraft {
	draftsByEmail := map[string]*accountEmailProjectionDraft{}
	add := func(email string, mutate func(*accountEmailProjectionDraft)) {
		displayEmail := strings.TrimSpace(email)
		normalizedEmail := emailutil.NormalizeAddressForDelivery(displayEmail)
		if !validProjectedAccountEmail(normalizedEmail) {
			return
		}
		draft := draftsByEmail[normalizedEmail]
		if draft == nil {
			draft = &accountEmailProjectionDraft{displayEmail: normalizedEmail}
			draftsByEmail[normalizedEmail] = draft
		}
		mutate(draft)
	}

	if identity == nil {
		return draftsByEmail
	}
	currentEmail := emailutil.NormalizeAddressForDelivery(identity.CurrentEmail())
	if currentEmail != "" {
		add(currentEmail, func(draft *accountEmailProjectionDraft) {
			draft.identityVerified = identity.CurrentEmailVerified()
			draft.providerSources = append(draft.providerSources, model.AccountEmailSource{
				SourceType: model.AccountEmailSourceTypeIdentityCurrent,
			})
		})
	}
	for _, address := range identity.VerifiableAddresses {
		if !strings.EqualFold(strings.TrimSpace(address.Via), "email") {
			continue
		}
		add(address.Value, func(draft *accountEmailProjectionDraft) {
			draft.identityVerified = draft.identityVerified || address.Verified
			draft.providerSources = append(draft.providerSources, model.AccountEmailSource{
				SourceType: model.AccountEmailSourceTypeIdentityCurrent,
			})
		})
	}
	if pendingEmail := identity.PendingEmail(); pendingEmail != "" {
		add(pendingEmail, func(draft *accountEmailProjectionDraft) {
			draft.providerSources = append(draft.providerSources, model.AccountEmailSource{
				SourceType: model.AccountEmailSourceTypeIdentityCurrent,
			})
		})
	}
	if credential, ok := identity.Credentials["code"]; ok && auth.CodeCredentialHasAddress(credential, currentEmail) {
		add(currentEmail, func(draft *accountEmailProjectionDraft) {
			draft.emailCodeUsable = true
			draft.providerSources = append(draft.providerSources, model.AccountEmailSource{
				SourceType: model.AccountEmailSourceTypeEmailCode,
			})
		})
	}
	for _, credential := range auth.NewCredentialInventory(identity.Credentials).OIDCProviders() {
		email := credential.VerifiedAccountEmail()
		if email == "" {
			continue
		}
		provider := strings.TrimSpace(credential.Provider)
		subject := strings.TrimSpace(credential.Subject)
		add(email, func(draft *accountEmailProjectionDraft) {
			draft.oidcVerified = true
			draft.providerSources = append(draft.providerSources, model.AccountEmailSource{
				SourceType:      model.AccountEmailSourceTypeOIDCProvider,
				Provider:        optionalAccountEmailString(provider),
				ProviderSubject: optionalAccountEmailString(subject),
			})
		})
	}
	for _, candidate := range providerCandidates {
		provider := auth.NormalizeOIDCProvider(candidate.Provider)
		subject := strings.TrimSpace(candidate.ProviderSubject)
		add(candidate.Email, func(draft *accountEmailProjectionDraft) {
			draft.oidcVerified = draft.oidcVerified || candidate.Verified
			draft.providerSources = append(draft.providerSources, model.AccountEmailSource{
				SourceType:      model.AccountEmailSourceTypeOIDCProvider,
				Provider:        optionalAccountEmailString(provider),
				ProviderSubject: optionalAccountEmailString(subject),
			})
		})
	}
	if draft := draftsByEmail[currentEmail]; draft != nil {
		draft.isCurrentEmail = true
	}
	return draftsByEmail
}

func projectedAccountEmailRows(
	identity *auth.Identity,
	providerCandidates []AccountEmailProviderCandidate,
) []AccountEmailProjection {
	drafts := buildAccountEmailProjectionDrafts(identity, providerCandidates)
	rows := make([]AccountEmailProjection, 0, len(drafts))
	for normalizedEmail, draft := range drafts {
		// A raw Kratos verified_address records a completed verification flow,
		// but it is not an independent long-lived source. Final delivery
		// eligibility is backed by an address-specific Email Code credential or
		// a currently connected provider assertion.
		trusted := draft.emailCodeUsable || draft.oidcVerified
		rows = append(rows, AccountEmailProjection{
			DisplayEmail:      normalizedEmail,
			NormalizedEmail:   normalizedEmail,
			IsCurrentEmail:    draft.isCurrentEmail,
			IdentityVerified:  draft.identityVerified,
			EffectiveTrusted:  trusted,
			UsableForDelivery: trusted,
			Sources:           dedupeAccountEmailSources(draft.providerSources),
		})
	}
	sortAccountEmailProjectionRows(rows)
	return rows
}

// ProjectedAccountEmailRows derives usable candidates from the authoritative
// Identity credential view.
func ProjectedAccountEmailRows(
	identity *auth.Identity,
	providerCandidates []AccountEmailProviderCandidate,
) []AccountEmailProjection {
	return projectedAccountEmailRows(identity, providerCandidates)
}

func findProjectionRow(rows []AccountEmailProjection, email string) *AccountEmailProjection {
	normalizedEmail := emailutil.NormalizeAddressForDelivery(email)
	for i := range rows {
		if rows[i].NormalizedEmail == normalizedEmail {
			return &rows[i]
		}
	}
	return nil
}

// FindAccountEmailProjection returns the exact normalized candidate.
func FindAccountEmailProjection(rows []AccountEmailProjection, email string) *AccountEmailProjection {
	return findProjectionRow(rows, email)
}

func dedupeAccountEmailSources(sources []model.AccountEmailSource) []model.AccountEmailSource {
	seen := map[string]bool{}
	result := make([]model.AccountEmailSource, 0, len(sources))
	for _, source := range sources {
		provider := strings.ToLower(strings.TrimSpace(ptrStringValue(source.Provider)))
		subject := strings.TrimSpace(ptrStringValue(source.ProviderSubject))
		key := string(source.SourceType) + ":" + provider + ":" + subject
		if seen[key] {
			continue
		}
		seen[key] = true
		source.Provider = optionalAccountEmailString(provider)
		source.ProviderSubject = optionalAccountEmailString(subject)
		result = append(result, source)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].SourceType != result[j].SourceType {
			return result[i].SourceType < result[j].SourceType
		}
		if ptrStringValue(result[i].Provider) != ptrStringValue(result[j].Provider) {
			return ptrStringValue(result[i].Provider) < ptrStringValue(result[j].Provider)
		}
		return ptrStringValue(result[i].ProviderSubject) < ptrStringValue(result[j].ProviderSubject)
	})
	return result
}

func sortAccountEmailProjectionRows(rows []AccountEmailProjection) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].UsableForDelivery != rows[j].UsableForDelivery {
			return rows[i].UsableForDelivery
		}
		if rows[i].IsCurrentEmail != rows[j].IsCurrentEmail {
			return rows[i].IsCurrentEmail
		}
		return rows[i].NormalizedEmail < rows[j].NormalizedEmail
	})
}

func validProjectedAccountEmail(email string) bool {
	return email != "" && len(email) <= 254 && strings.Contains(email, "@") && !strings.HasSuffix(email, ".local")
}

func optionalAccountEmailString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
