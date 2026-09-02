package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

type AccountCredentialKind string

const (
	AccountCredentialOIDC    AccountCredentialKind = "oidc"
	AccountCredentialPasskey AccountCredentialKind = "passkey"
)

var (
	ErrAccountCredentialSnapshotMissing   = errors.New("complete credential snapshots are required")
	ErrAccountCredentialCommittedMismatch = errors.New("hook credential snapshot does not match committed identity")
	ErrAccountCredentialMutationShape     = errors.New("credential mutation is not one catalog transition")
	ErrAccountCredentialUnrecoverable     = errors.New("credential mutation leaves no recoverable authentication method")
)

type AccountCredentialHookInput struct {
	AuditID                   string
	FlowID                    string
	IdentityID                string
	Kind                      AccountCredentialKind
	PreviousCredentials       map[string]auth.Credential
	Credentials               map[string]auth.Credential
	PreviousSnapshotPresent   bool
	CredentialSnapshotPresent bool
}

type accountCredentialHookIdentityManager interface {
	auth.IdentityManager
}

// AccountCredentialHookLifecycle owns the policy-before-commit and
// completion-after-commit boundary for Kratos settings mutations.
type AccountCredentialHookLifecycle struct {
	db           *gorm.DB
	identity     accountCredentialHookIdentityManager
	auditWriter  domainaudit.Appender
	publisher    EmailCommandPublisher
	memberEmails MemberEmailProjection
}

func NewAccountCredentialHookLifecycle(
	db *gorm.DB,
	identity accountCredentialHookIdentityManager,
	auditWriter domainaudit.Appender,
	publisher EmailCommandPublisher,
	memberEmails MemberEmailProjection,
) *AccountCredentialHookLifecycle {
	if db == nil || identity == nil || auditWriter == nil || publisher == nil {
		panic("account credential hook database, identity, audit writer, and publisher are required")
	}
	if memberEmails == nil {
		panic("account credential hook member email projection is required")
	}
	return &AccountCredentialHookLifecycle{
		db: db, identity: identity, auditWriter: auditWriter, publisher: publisher,
		memberEmails: memberEmails,
	}
}

func (s *AccountCredentialHookLifecycle) Validate(
	ctx context.Context,
	input AccountCredentialHookInput,
) error {
	if err := validateAccountCredentialHookInput(input, true, false); err != nil {
		return err
	}
	if _, _, err := accountCredentialMutationFromSnapshots(input); err != nil {
		return err
	}
	if !auth.NewCredentialInventory(input.Credentials).HasRecoverableAuthenticationMethod() {
		return ErrAccountCredentialUnrecoverable
	}
	if input.Kind != AccountCredentialOIDC {
		return nil
	}
	identity, err := s.identity.GetIdentity(ctx, input.IdentityID)
	if err != nil {
		return err
	}
	if identity == nil || identity.ID != input.IdentityID {
		return fmt.Errorf("identity %s was not returned", input.IdentityID)
	}
	proposedIdentity := *identity
	proposedIdentity.Credentials = input.Credentials
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, input.Credentials)
	return NewAccountEmailService(s.db, s.identity, s.memberEmails).
		EnsureMemberPrimaryEmailUsable(ctx, input.IdentityID, &proposedIdentity, providerCandidates)
}

func (s *AccountCredentialHookLifecycle) Complete(
	ctx context.Context,
	input AccountCredentialHookInput,
) error {
	if err := validateAccountCredentialHookInput(input, true, true); err != nil {
		return err
	}
	committed, err := loadIdentityAuthenticationCredentials(ctx, s.identity, input.IdentityID)
	if err != nil {
		return err
	}
	if !credentialSnapshotsEqual(committed.Credentials, input.Credentials) {
		return ErrAccountCredentialCommittedMismatch
	}

	mutation, changed, err := accountCredentialMutationFromSnapshots(input)
	if err != nil || !changed {
		return err
	}
	memberID := strings.TrimSpace(committed.ExternalID)
	if memberID == "" {
		return fmt.Errorf("committed identity has no Member link")
	}
	actor, err := sharedtelemetry.ActorForRecord(sharedtelemetry.MemberActor{MemberID: memberID})
	if err != nil {
		return err
	}
	metadata := sharedtelemetry.AuditMetadata{
		AuditID: input.AuditID, OccurredAt: time.Now().UTC(),
		Correlation: sharedtelemetry.CorrelationFromContext(ctx), RecordActor: actor,
	}
	record, err := mutation.buildAudit(metadata, memberID)
	if err != nil {
		return err
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockActiveCredentialMutationMember(ctx, tx, memberID, input.IdentityID); err != nil {
			return err
		}
		if input.Kind == AccountCredentialOIDC {
			providerCandidates := ResolveAccountEmailProviderCandidates(ctx, committed.Credentials)
			if _, err := NewAccountEmailService(tx, s.identity, s.memberEmails).
				SyncMemberEmailProjection(ctx, input.IdentityID, committed, providerCandidates); err != nil {
				return err
			}
		}
		return s.auditWriter.AppendDomainAuditInTransaction(ctx, tx, record)
	}); err != nil {
		return err
	}

	if mutation.event == email.EventSocialLoginRemoved {
		if err := s.identity.DeleteIdentitySessions(ctx, input.IdentityID); err != nil {
			return err
		}
	}
	return PublishAccountSecurityEventEmail(
		ctx,
		s.publisher,
		s.db,
		s.memberEmails,
		s.identity,
		input.IdentityID,
		mutation.event,
		input.AuditID,
		mutation.emailData,
	)
}

func validateAccountCredentialHookInput(
	input AccountCredentialHookInput,
	requirePrevious bool,
	requireAuditID bool,
) error {
	if strings.TrimSpace(input.IdentityID) == "" || strings.TrimSpace(input.FlowID) == "" {
		return fmt.Errorf("identity_id and flow_id are required")
	}
	switch input.Kind {
	case AccountCredentialOIDC, AccountCredentialPasskey:
	default:
		return fmt.Errorf("unsupported credential kind %q", input.Kind)
	}
	if !input.CredentialSnapshotPresent || input.Credentials == nil {
		return ErrAccountCredentialSnapshotMissing
	}
	if requirePrevious {
		if !input.PreviousSnapshotPresent || input.PreviousCredentials == nil {
			return ErrAccountCredentialSnapshotMissing
		}
	}
	if requireAuditID && strings.TrimSpace(input.AuditID) == "" {
		return fmt.Errorf("audit id is required")
	}
	return nil
}

type accountCredentialMutation struct {
	event      email.EventKey
	provider   string
	subject    string
	passkeyIDs []string
	emailData  map[string]string
}

func (mutation accountCredentialMutation) buildAudit(
	metadata sharedtelemetry.AuditMetadata,
	memberID string,
) (sharedtelemetry.AuditRecord, error) {
	switch mutation.event {
	case email.EventSocialLoginAdded:
		return sharedtelemetry.NewAccountSocialLoginAddedAuditRecord(metadata, memberID, mutation.provider, mutation.subject)
	case email.EventSocialLoginRemoved:
		return sharedtelemetry.NewAccountSocialLoginRemovedAuditRecord(metadata, memberID, mutation.provider, mutation.subject)
	case email.EventPasskeyAdded:
		return sharedtelemetry.NewAccountPasskeyAddedAuditRecord(metadata, memberID, mutation.passkeyIDs)
	case email.EventPasskeyRemoved:
		return sharedtelemetry.NewAccountPasskeyRemovedAuditRecord(metadata, memberID, mutation.passkeyIDs)
	default:
		return sharedtelemetry.AuditRecord{}, ErrAccountCredentialMutationShape
	}
}

func accountCredentialMutationFromSnapshots(
	input AccountCredentialHookInput,
) (accountCredentialMutation, bool, error) {
	switch input.Kind {
	case AccountCredentialOIDC:
		if !slices.Equal(
			canonicalCredentialValues(auth.CodeCredentialEmails(input.PreviousCredentials["code"])),
			canonicalCredentialValues(auth.CodeCredentialEmails(input.Credentials["code"])),
		) || !slices.Equal(
			auth.PasskeyCredentialIDs(input.PreviousCredentials["passkey"]),
			auth.PasskeyCredentialIDs(input.Credentials["passkey"]),
		) {
			return accountCredentialMutation{}, false, ErrAccountCredentialMutationShape
		}
		before := oidcProviderKeys(input.PreviousCredentials)
		after := oidcProviderKeys(input.Credentials)
		added, removed := stringSetDiff(before, after)
		if len(added) == 0 && len(removed) == 0 {
			return accountCredentialMutation{}, false, nil
		}
		if len(added)+len(removed) != 1 {
			return accountCredentialMutation{}, false, ErrAccountCredentialMutationShape
		}
		event := email.EventSocialLoginAdded
		var key string
		if len(added) == 1 {
			key = added[0]
		}
		if len(removed) == 1 {
			event = email.EventSocialLoginRemoved
			key = removed[0]
		}
		provider, subject, ok := strings.Cut(key, ":")
		if !ok || provider == "" || subject == "" {
			return accountCredentialMutation{}, false, ErrAccountCredentialMutationShape
		}
		return accountCredentialMutation{
			event: event, provider: provider, subject: subject,
			emailData: map[string]string{"provider": provider, "_provider_subject": subject},
		}, true, nil
	case AccountCredentialPasskey:
		if !slices.Equal(
			canonicalCredentialValues(auth.CodeCredentialEmails(input.PreviousCredentials["code"])),
			canonicalCredentialValues(auth.CodeCredentialEmails(input.Credentials["code"])),
		) || !slices.Equal(
			oidcProviderKeys(input.PreviousCredentials),
			oidcProviderKeys(input.Credentials),
		) {
			return accountCredentialMutation{}, false, ErrAccountCredentialMutationShape
		}
		before := auth.PasskeyCredentialIDs(input.PreviousCredentials["passkey"])
		after := auth.PasskeyCredentialIDs(input.Credentials["passkey"])
		added, removed := stringSetDiff(before, after)
		if len(added) == 0 && len(removed) == 0 {
			return accountCredentialMutation{}, false, nil
		}
		if len(added) > 0 && len(removed) > 0 {
			return accountCredentialMutation{}, false, ErrAccountCredentialMutationShape
		}
		event := email.EventPasskeyAdded
		changedIDs := added
		if len(removed) > 0 {
			event = email.EventPasskeyRemoved
			changedIDs = removed
		}
		return accountCredentialMutation{
			event: event, passkeyIDs: changedIDs,
			emailData: map[string]string{"_credential_count": fmt.Sprintf("%d", len(after))},
		}, true, nil
	default:
		return accountCredentialMutation{}, false, ErrAccountCredentialMutationShape
	}
}

func credentialSnapshotsEqual(left, right map[string]auth.Credential) bool {
	return slices.Equal(oidcProviderKeys(left), oidcProviderKeys(right)) &&
		slices.Equal(canonicalCredentialValues(auth.CodeCredentialEmails(left["code"])), canonicalCredentialValues(auth.CodeCredentialEmails(right["code"]))) &&
		slices.Equal(auth.PasskeyCredentialIDs(left["passkey"]), auth.PasskeyCredentialIDs(right["passkey"]))
}

func oidcProviderKeys(credentials map[string]auth.Credential) []string {
	providers := auth.NewCredentialInventory(credentials).OIDCProviders()
	keys := make([]string, 0, len(providers))
	for _, provider := range providers {
		key := auth.CanonicalOIDCProviderIdentifier(provider.Provider, provider.Subject)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return canonicalCredentialValues(keys)
}

func canonicalCredentialValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func stringSetDiff(before, after []string) (added, removed []string) {
	beforeSet := make(map[string]struct{}, len(before))
	afterSet := make(map[string]struct{}, len(after))
	for _, value := range before {
		beforeSet[value] = struct{}{}
	}
	for _, value := range after {
		afterSet[value] = struct{}{}
		if _, exists := beforeSet[value]; !exists {
			added = append(added, value)
		}
	}
	for _, value := range before {
		if _, exists := afterSet[value]; !exists {
			removed = append(removed, value)
		}
	}
	return added, removed
}

func lockActiveCredentialMutationMember(
	ctx context.Context,
	tx *gorm.DB,
	memberID string,
	identityID string,
) error {
	return authorizationtarget.LockActivePairForCredentialMutation(ctx, tx, memberID, identityID)
}
