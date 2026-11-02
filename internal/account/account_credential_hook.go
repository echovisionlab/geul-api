package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
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
	auth.IdentityGetter
	auth.IdentitySessionRevoker
}

// AccountCredentialHookLifecycle owns the policy-before-commit and
// completion-after-commit boundary for Kratos settings mutations.
type AccountCredentialHookLifecycle struct {
	units       Units
	identity    accountCredentialHookIdentityManager
	auditWriter domainaudit.Appender
	publisher   EmailPublisher
}

func NewAccountCredentialHookLifecycle(
	units Units,
	identity accountCredentialHookIdentityManager,
	auditWriter domainaudit.Appender,
	publisher EmailPublisher,
) *AccountCredentialHookLifecycle {
	if units == nil || identity == nil || auditWriter == nil || publisher == nil {
		panic("account credential hook units, identity, audit writer, and publisher are required")
	}
	return &AccountCredentialHookLifecycle{units: units, identity: identity, auditWriter: auditWriter, publisher: publisher}
}

// read runs one read of the hook in a transaction of its own.
func (s *AccountCredentialHookLifecycle) read(ctx context.Context, work func(Store) error) error {
	return s.units.Transaction(ctx, false, func(tx mutation.Tx[Store]) error { return work(tx.Store()) })
}

func (s *AccountCredentialHookLifecycle) Validate(
	ctx context.Context,
	input AccountCredentialHookInput,
) (err error) {
	defer func() {
		if err != nil {
			err = annotateCredentialHookFailure("validate", input, err)
		}
	}()
	if err := validateAccountCredentialHookInput(input, true, false); err != nil {
		return err
	}
	transition, changed, err := accountCredentialMutationFromSnapshots(input)
	if err != nil {
		return err
	}
	if !auth.NewCredentialInventory(input.Credentials).HasRecoverableAuthenticationMethod() {
		return ErrAccountCredentialUnrecoverable
	}
	if input.Kind != AccountCredentialOIDC || !changed || transition.event != email.EventSocialLoginRemoved {
		return nil
	}
	identity, err := loadIdentityAuthenticationCredentials(ctx, s.identity, input.IdentityID)
	if err != nil {
		return err
	}
	if identity == nil || identity.ID != input.IdentityID {
		return fmt.Errorf("identity %s was not returned", input.IdentityID)
	}
	// The webhook carries inventory, never provider tokens or claims. Load
	// provider proof through the existing confidential identity port and remove
	// only the provider identified by the validated transition.
	proposedIdentity := identityWithoutOIDCProvider(identity, transition.provider, transition.subject)
	providerCandidates := ResolveAccountEmailProviderCandidates(ctx, proposedIdentity.Credentials)
	return newAccountEmailService(s.identity, s.read).
		EnsureMemberPrimaryEmailUsable(ctx, input.IdentityID, proposedIdentity, providerCandidates)
}

func (s *AccountCredentialHookLifecycle) Complete(
	ctx context.Context,
	input AccountCredentialHookInput,
) (err error) {
	defer func() {
		if err != nil {
			err = annotateCredentialHookFailure("complete", input, err)
		}
	}()
	if err := validateAccountCredentialHookInput(input, true, true); err != nil {
		return err
	}
	return s.units.Fenced(ctx, input.IdentityID, func(ctx context.Context, steps Steps) error {
		return s.complete(ctx, steps, input)
	})
}

func (s *AccountCredentialHookLifecycle) complete(ctx context.Context, steps Steps, input AccountCredentialHookInput) error {
	committed, err := loadIdentityAuthenticationCredentials(ctx, s.identity, input.IdentityID)
	if err != nil {
		return err
	}
	if !credentialSnapshotsEqual(committed.Credentials, input.Credentials) {
		return ErrAccountCredentialCommittedMismatch
	}

	transition, changed, err := accountCredentialMutationFromSnapshots(input)
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
	record, err := transition.buildAudit(metadata, memberID)
	if err != nil {
		return err
	}

	// One transaction locks the pair, converges the Member email projection
	// and appends the audit record that makes a replay idempotent.
	if err := steps.Step(ctx, func(store Store) error {
		if err := store.LockActivePairForCredentialMutation(ctx, memberID, input.IdentityID); err != nil {
			return err
		}
		if input.Kind == AccountCredentialOIDC {
			providerCandidates := ResolveAccountEmailProviderCandidates(ctx, committed.Credentials)
			inTransaction := func(_ context.Context, work func(Store) error) error { return work(store) }
			if _, err := newAccountEmailService(s.identity, inTransaction).
				SyncMemberEmailProjection(ctx, input.IdentityID, committed, providerCandidates); err != nil {
				return err
			}
		}
		return store.AppendAuditRecord(ctx, s.auditWriter, record)
	}); err != nil {
		return err
	}

	if transition.event == email.EventSocialLoginRemoved {
		if err := s.identity.DeleteIdentitySessions(ctx, input.IdentityID); err != nil {
			return err
		}
	}
	return PublishAccountSecurityEventEmail(
		ctx,
		s.publisher,
		steps.Step,
		s.identity,
		input.IdentityID,
		transition.event,
		input.AuditID,
		transition.emailData,
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

func (transition accountCredentialMutation) buildAudit(
	metadata sharedtelemetry.AuditMetadata,
	memberID string,
) (sharedtelemetry.AuditRecord, error) {
	switch transition.event {
	case email.EventSocialLoginAdded:
		return sharedtelemetry.NewAccountSocialLoginAddedAuditRecord(metadata, memberID, transition.provider, transition.subject)
	case email.EventSocialLoginRemoved:
		return sharedtelemetry.NewAccountSocialLoginRemovedAuditRecord(metadata, memberID, transition.provider, transition.subject)
	case email.EventPasskeyAdded:
		return sharedtelemetry.NewAccountPasskeyAddedAuditRecord(metadata, memberID, transition.passkeyIDs)
	case email.EventPasskeyRemoved:
		return sharedtelemetry.NewAccountPasskeyRemovedAuditRecord(metadata, memberID, transition.passkeyIDs)
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

// annotateCredentialHookFailure attaches the diagnosis the hook endpoint's
// failure record needs after telemetry redaction, and marks provider subjects,
// tokens, and email data private so no record keeps them.
func annotateCredentialHookFailure(stage string, input AccountCredentialHookInput, err error) error {
	code := "credential_hook_failed"
	switch {
	case errors.Is(err, ErrAccountCredentialSnapshotMissing):
		code = "credential_snapshot_missing"
	case errors.Is(err, ErrAccountCredentialCommittedMismatch):
		code = "credential_committed_mismatch"
	case errors.Is(err, ErrAccountCredentialMutationShape):
		code = "credential_mutation_invalid"
	case errors.Is(err, ErrAccountCredentialUnrecoverable):
		code = "recoverable_auth_method"
	case errors.Is(err, ErrMemberPrimaryEmailUnavailable):
		code = "canonical_email_provider_required"
	}
	var privateValues []string
	for _, credentials := range []map[string]auth.Credential{input.PreviousCredentials, input.Credentials} {
		for _, provider := range auth.NewCredentialInventory(credentials).OIDCProviders() {
			privateValues = append(privateValues, provider.Subject, provider.InitialAccessToken)
			privateValues = append(privateValues, provider.NicknameCandidates()...)
		}
	}
	if transition, changed, mutationErr := accountCredentialMutationFromSnapshots(input); mutationErr == nil && changed {
		for _, value := range transition.emailData {
			privateValues = append(privateValues, value)
		}
	}
	return failurecause.WithAttrs(
		failurecause.WithPrivate(err, privateValues...),
		slog.String("error_code", code),
		slog.String("stage", stage),
		slog.String("credential_type", string(input.Kind)),
		slog.Bool("proposed_snapshot_present", input.CredentialSnapshotPresent && input.Credentials != nil),
		slog.Bool("previous_snapshot_present", input.PreviousSnapshotPresent && input.PreviousCredentials != nil),
	)
}
