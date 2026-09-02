package account

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

// AccountCredentialMutationService owns credential inventory validation,
// credential mutation, session revocation, and the resulting email projection.
// Handlers must not reproduce this ordering.
type AccountCredentialMutationService struct {
	db           *gorm.DB
	identity     accountCredentialIdentityManager
	memberEmails MemberEmailProjection
}

type accountCredentialIdentityManager interface {
	auth.IdentityManager
	auth.IdentityCredentialIdentifierDeleter
	auth.SessionRevoker
}

func NewAccountCredentialMutationService(
	db *gorm.DB,
	identity accountCredentialIdentityManager,
	memberEmails MemberEmailProjection,
) *AccountCredentialMutationService {
	if db == nil {
		panic("account credential mutation database is required")
	}
	if identity == nil {
		panic("account credential mutation identity manager is required")
	}
	if memberEmails == nil {
		panic("account credential mutation member email projection is required")
	}
	return &AccountCredentialMutationService{db: db, identity: identity, memberEmails: memberEmails}
}

func (s *AccountCredentialMutationService) LoadIdentity(
	ctx context.Context,
	identityID string,
) (*auth.Identity, error) {
	return loadIdentityAuthenticationCredentials(ctx, s.identity, identityID)
}

func loadIdentityAuthenticationCredentials(
	ctx context.Context,
	identityReader auth.IdentityReader,
	identityID string,
) (*auth.Identity, error) {
	identity, err := loadIdentityWithCredentials(ctx, identityReader, identityID, "oidc", "code", "passkey")
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, fmt.Errorf("identity %s was not found", identityID)
	}
	return identity, nil
}

func (s *AccountCredentialMutationService) RemoveOIDCProvider(
	ctx context.Context,
	identityID string,
	provider string,
	identifier string,
) error {
	provider = auth.NormalizeOIDCProvider(provider)
	identifier = strings.TrimSpace(identifier)
	if provider == "" || identifier == "" {
		return errs.FailedPrecondition("SSO provider identity is incomplete")
	}
	return identitystate.WithMutation(ctx, s.db, identityID, func(mutationCtx context.Context, tx *gorm.DB) error {
		identity, err := s.LoadIdentity(mutationCtx, identityID)
		if err != nil {
			return err
		}
		inventory := auth.NewCredentialInventory(identity.Credentials)
		if err := s.removeOIDCProviderIfPresent(
			mutationCtx, identityID, provider, identifier, identity, inventory,
		); err != nil {
			return err
		}
		updated, err := s.LoadIdentity(mutationCtx, identityID)
		if err != nil {
			return err
		}
		providerCandidates := ResolveAccountEmailProviderCandidates(mutationCtx, updated.Credentials)
		_, err = NewAccountEmailService(tx, s.identity, s.memberEmails).
			SyncMemberEmailProjection(mutationCtx, identityID, updated, providerCandidates)
		return err
	})
}

func (s *AccountCredentialMutationService) removeOIDCProviderIfPresent(
	ctx context.Context,
	identityID string,
	provider string,
	identifier string,
	identity *auth.Identity,
	inventory auth.CredentialInventory,
) error {
	if !inventory.HasOIDCProvider(provider, identifier) {
		return nil
	}
	if inventory.RecoverableAuthenticationMethodCountAfterOIDCRemoval(provider, identifier) == 0 {
		return errs.FailedPrecondition("keep email sign-in or another social sign-in method before removing this provider")
	}
	if err := validateCanonicalEmailAfterOIDCRemoval(ctx, provider, identifier, identity); err != nil {
		return err
	}
	// Revoke live sessions before credential removal. A revocation failure leaves
	// the credential intact; a retry repairs the identity-derived projection.
	if err := s.identity.DeleteIdentitySessions(ctx, identityID); err != nil {
		return err
	}
	return s.identity.DeleteIdentityCredentialIdentifier(
		ctx,
		identityID,
		"oidc",
		auth.CanonicalOIDCProviderIdentifier(provider, identifier),
	)
}

func validateCanonicalEmailAfterOIDCRemoval(
	ctx context.Context,
	provider string,
	identifier string,
	identity *auth.Identity,
) error {
	canonical := emailutil.NormalizeAddressForDelivery(identity.CurrentEmail())
	if canonical == "" {
		return nil
	}
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, identity.Credentials)
	current := findProjectionRow(projectedAccountEmailRows(identity, providerCandidates), canonical)
	if current == nil || !hasOIDCProviderSource(*current, provider, identifier) {
		return nil
	}
	withoutProvider := identityWithoutOIDCProvider(identity, provider, identifier)
	afterCandidates := ResolveAccountEmailProviderCandidates(ctx, withoutProvider.Credentials)
	after := findProjectionRow(projectedAccountEmailRows(withoutProvider, afterCandidates), canonical)
	if after == nil || !hasIndependentAccountEmailProof(*after) {
		return errs.FailedPrecondition("choose another account email before removing the provider that proves the current account email")
	}
	return nil
}

func hasIndependentAccountEmailProof(row AccountEmailProjection) bool {
	for _, source := range row.Sources {
		if source.SourceType == model.AccountEmailSourceTypeEmailCode || source.SourceType == model.AccountEmailSourceTypeOIDCProvider {
			return true
		}
	}
	return false
}

func hasOIDCProviderSource(row AccountEmailProjection, provider, subject string) bool {
	provider = auth.NormalizeOIDCProvider(provider)
	subject = strings.TrimSpace(subject)
	for _, source := range row.Sources {
		if source.SourceType != model.AccountEmailSourceTypeOIDCProvider {
			continue
		}
		if auth.NormalizeOIDCProvider(ptrStringValue(source.Provider)) == provider && strings.TrimSpace(ptrStringValue(source.ProviderSubject)) == subject {
			return true
		}
	}
	return false
}

func identityWithoutOIDCProvider(identity *auth.Identity, provider, subject string) *auth.Identity {
	if identity == nil {
		return nil
	}
	clone := *identity
	clone.Credentials = make(map[string]auth.Credential, len(identity.Credentials))
	maps.Copy(clone.Credentials, identity.Credentials)
	oidc, ok := identity.Credentials["oidc"]
	if !ok {
		return &clone
	}
	filtered := oidc
	filtered.Identifiers = make([]string, 0, len(oidc.Identifiers))
	for _, identifier := range oidc.Identifiers {
		if strings.EqualFold(strings.TrimSpace(identifier), auth.CanonicalOIDCProviderIdentifier(provider, subject)) {
			continue
		}
		filtered.Identifiers = append(filtered.Identifiers, identifier)
	}
	if oidc.Config != nil {
		filtered.Config = oidcConfigWithoutProvider(oidc.Config, provider, subject)
	}
	clone.Credentials["oidc"] = filtered
	return &clone
}

func oidcConfigWithoutProvider(config structured.Fields, provider, subject string) structured.Fields {
	filtered := make(structured.Fields, len(config))
	maps.Copy(filtered, config)
	rawProviders, ok := config["providers"].(structured.Values)
	if !ok {
		return filtered
	}
	providers := make(structured.Values, 0, len(rawProviders))
	provider = auth.NormalizeOIDCProvider(provider)
	subject = strings.TrimSpace(subject)
	for _, rawProvider := range rawProviders {
		candidate, ok := rawProvider.(structured.Fields)
		if ok && auth.NormalizeOIDCProvider(credentialStringValue(candidate["provider"])) == provider &&
			strings.TrimSpace(credentialStringValue(candidate["subject"])) == subject {
			continue
		}
		providers = append(providers, rawProvider)
	}
	filtered["providers"] = providers
	return filtered
}

func credentialStringValue(value structured.Value) string {
	result, _ := value.(string)
	return result
}

func inventoryCredential(credentials map[string]auth.Credential, credentialType string) auth.Credential {
	if credentials == nil {
		return auth.Credential{Type: credentialType}
	}
	if credential, ok := credentials[credentialType]; ok {
		return credential
	}
	return auth.Credential{Type: credentialType}
}
