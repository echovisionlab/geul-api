package oauthapp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateCommand registers a sign-in app.
type CreateCommand struct {
	// ClientID is the chosen client ID; empty generates one.
	ClientID string
	Settings Settings
}

// UpdateCommand replaces every setting of a sign-in app.
type UpdateCommand struct {
	ClientID         string
	ExpectedRevision string
	Settings         Settings
}

// AppCommand names a sign-in app at the revision its caller read.
type AppCommand struct {
	ClientID         string
	ExpectedRevision string
}

// SavedApp is a written sign-in app and the secret it was just given, shown
// once.
type SavedApp struct {
	App    App
	Secret *string
}

// write is what a sign-in app mutation stored.
type write struct {
	record Record
	fields []string
}

// CreateSignInApp registers the Hydra client and records who may sign in.
func (s *Service) CreateSignInApp(ctx context.Context, command CreateCommand) (SavedApp, error) {
	clientID, err := normalizeClientID(command.ClientID)
	if err != nil {
		return SavedApp{}, err
	}
	settings, err := normalizeSettings(command.Settings)
	if err != nil {
		return SavedApp{}, err
	}
	criteria, err := s.requireEligibleCriteria(ctx, settings.MemberCriteriaIDs, nil)
	if err != nil {
		return SavedApp{}, err
	}
	var secret *string
	client := clientOf(clientID, settings)
	if settings.AuthMethod.usesSecret() {
		if client.Secret, err = s.newSecret(); err != nil {
			return SavedApp{}, err
		}
		secret = &client.Secret
	}
	registered := false
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[write, Store]{
		Access: mutation.FreshAdminCan(policyv1.OauthSignInApp.Create()),
		Change: func(ctx context.Context, store Store) (write, error) {
			record, err := store.InsertApp(ctx, Record{ClientID: clientID, Access: settings.Access, MemberCriteriaIDs: settings.MemberCriteriaIDs})
			if err != nil {
				return write{}, duplicateApp(err, clientID)
			}
			if err := s.clients.Create(ctx, client); err != nil {
				if errors.Is(err, ErrClientExists) {
					return write{}, duplicateApp(err, clientID)
				}
				return write{}, hydraUnavailable(err)
			}
			registered = true
			return write{record: record}, nil
		},
		Audit: func(result write) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditOAuthSignInAppCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewOAuthSignInAppCreatedAuditRecord(metadata, result.record.ClientID)
			})
		},
	})
	if err != nil {
		if registered {
			// The Hydra client exists without its committed app; remove it.
			if removeErr := s.clients.Delete(context.WithoutCancel(ctx), clientID); removeErr != nil && !errors.Is(removeErr, ErrClientNotFound) {
				err = errors.Join(err, fmt.Errorf("remove the Hydra client of the failed sign-in app: %w", removeErr))
			}
		}
		return SavedApp{}, err
	}
	return SavedApp{App: appOf(result.record, settings, criteria), Secret: secret}, nil
}

// UpdateSignInApp replaces the app's settings. Turning a public app into one
// that authenticates with a secret issues a new secret.
func (s *Service) UpdateSignInApp(ctx context.Context, command UpdateCommand) (SavedApp, error) {
	settings, err := normalizeSettings(command.Settings)
	if err != nil {
		return SavedApp{}, err
	}
	clientID := strings.TrimSpace(command.ClientID)
	var current Record
	var before Client
	var secret *string
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[write, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			current, err = lockAppAt(ctx, store, clientID, command.ExpectedRevision)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.OauthSignInApp.Update()),
		Change: func(ctx context.Context, store Store) (write, error) {
			if before, err = s.existingClient(ctx, clientID); err != nil {
				return write{}, err
			}
			if _, err := s.requireEligibleCriteria(ctx, settings.MemberCriteriaIDs, current.MemberCriteriaIDs); err != nil {
				return write{}, err
			}
			fields := changedFields(settingsOf(current, before), settings)
			client := clientOf(clientID, settings)
			if settings.AuthMethod.usesSecret() && !AuthMethod(before.AuthMethod).usesSecret() {
				if client.Secret, err = s.newSecret(); err != nil {
					return write{}, err
				}
				secret = &client.Secret
				fields = append(fields, "client_secret")
			}
			if len(fields) == 0 {
				return write{record: current}, nil
			}
			record, err := store.UpdateApp(ctx, Record{ClientID: clientID, Access: settings.Access, MemberCriteriaIDs: settings.MemberCriteriaIDs})
			if err != nil {
				return write{}, err
			}
			if err := s.clients.Replace(ctx, client); err != nil {
				return write{}, hydraUnavailable(err)
			}
			return write{record: record, fields: fields}, nil
		},
		Audit: updatedAudit,
	})
	if err != nil {
		return SavedApp{}, err
	}
	saved, err := s.app(ctx, result.record)
	return SavedApp{App: saved, Secret: secret}, err
}

// RotateSignInAppSecret replaces the client secret of an app that uses one.
// The previous secret stops working at once.
func (s *Service) RotateSignInAppSecret(ctx context.Context, command AppCommand) (SavedApp, error) {
	clientID := strings.TrimSpace(command.ClientID)
	var current Record
	var secret string
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[write, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			current, err = lockAppAt(ctx, store, clientID, command.ExpectedRevision)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.OauthSignInApp.RotateSecret()),
		Change: func(ctx context.Context, store Store) (write, error) {
			client, err := s.existingClient(ctx, clientID)
			if err != nil {
				return write{}, err
			}
			if !AuthMethod(client.AuthMethod).usesSecret() {
				return write{}, domainerr.Precondition("a public sign-in app has no client secret")
			}
			if secret, err = s.newSecret(); err != nil {
				return write{}, err
			}
			record, err := store.UpdateApp(ctx, current)
			if err != nil {
				return write{}, err
			}
			client.Secret = secret
			if err := s.clients.Replace(ctx, client); err != nil {
				return write{}, hydraUnavailable(err)
			}
			return write{record: record, fields: []string{"client_secret"}}, nil
		},
		Audit: updatedAudit,
	})
	if err != nil {
		return SavedApp{}, err
	}
	saved, err := s.app(ctx, result.record)
	return SavedApp{App: saved, Secret: &secret}, err
}

// DeleteSignInApp removes the app and its Hydra client; its tokens stop
// working.
func (s *Service) DeleteSignInApp(ctx context.Context, command AppCommand) error {
	clientID := strings.TrimSpace(command.ClientID)
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[write, Store]{
		Lock: func(ctx context.Context, store Store) error {
			_, err := lockAppAt(ctx, store, clientID, command.ExpectedRevision)
			return err
		},
		Access: mutation.FreshAdminCan(policyv1.OauthSignInApp.Delete()),
		Change: func(ctx context.Context, store Store) (write, error) {
			if err := store.DeleteApp(ctx, clientID); err != nil {
				return write{}, err
			}
			if err := s.clients.Delete(ctx, clientID); err != nil && !errors.Is(err, ErrClientNotFound) {
				return write{}, hydraUnavailable(err)
			}
			return write{record: Record{ClientID: clientID}}, nil
		},
		Audit: func(result write) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditOAuthSignInAppDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewOAuthSignInAppDeletedAuditRecord(metadata, result.record.ClientID)
			})
		},
	})
	return err
}

func updatedAudit(result write) mutation.Audit {
	if len(result.fields) == 0 {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditOAuthSignInAppUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewOAuthSignInAppUpdatedAuditRecord(metadata, result.record.ClientID, result.fields)
	})
}

// lockAppAt locks the app and requires the revision its caller read.
func lockAppAt(ctx context.Context, store Store, clientID, expectedRevision string) (Record, error) {
	if strings.TrimSpace(expectedRevision) == "" {
		return Record{}, domainerr.Required("expected_revision")
	}
	record, err := store.LockApp(ctx, clientID)
	if err != nil {
		return Record{}, appNotFound(err, clientID)
	}
	if record.Revision != strings.TrimSpace(expectedRevision) {
		return Record{}, domainerr.Retry("the sign-in app changed; read it again before saving")
	}
	return record, nil
}

// existingClient reads the app's Hydra client, which must still exist.
func (s *Service) existingClient(ctx context.Context, clientID string) (Client, error) {
	client, err := s.clients.Get(ctx, clientID)
	if errors.Is(err, ErrClientNotFound) {
		return Client{}, domainerr.Precondition("the sign-in app's OAuth client is missing; delete the app and register it again")
	}
	if err != nil {
		return Client{}, hydraUnavailable(err)
	}
	return client, nil
}

// requireEligibleCriteria returns the summaries of ids. Every criteria the
// app did not already name must exist and be able to admit Members; one it
// keeps may since have been archived and then admits nobody.
func (s *Service) requireEligibleCriteria(ctx context.Context, ids, kept []string) ([]CriteriaSummary, error) {
	byID, err := s.criteriaByID(ctx, slices.Clone(ids))
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		summary, exists := byID[id]
		if slices.Contains(kept, id) && exists {
			continue
		}
		if !exists || !summary.Eligible {
			return nil, domainerr.InvalidField(
				"settings.member_criteria_ids",
				"one or more Member criteria do not exist or cannot admit Members",
			)
		}
	}
	return pick(byID, ids), nil
}

func duplicateApp(err error, clientID string) error {
	if errors.Is(err, ErrAppExists) || errors.Is(err, ErrClientExists) {
		return domainerr.Duplicate("OAuth client", "client_id", clientID)
	}
	return err
}
