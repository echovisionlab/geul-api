package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateClientCommand creates a Client.
type CreateClientCommand struct {
	Name    string
	Website *string
}

// UpdateClientCommand changes the supplied fields of Client ID.
type UpdateClientCommand struct {
	ID      string
	Name    *string
	Website *string
}

// DeleteClientCommand deletes Client ID when nothing references it.
type DeleteClientCommand struct {
	ID string
}

func duplicateClientName(err error) error {
	if errors.Is(err, ErrClientNameTaken) {
		return domainerr.New(domainerr.AlreadyExists, "name", "client with this name already exists").WithCause(err)
	}
	return err
}

// lockClient takes the Client row for a mutation.
func lockClient(ctx context.Context, store Store, id string) (Client, error) {
	client, err := store.LockClient(ctx, id)
	if errors.Is(err, ErrClientNotFound) {
		return Client{}, domainerr.ResourceNotFound("client", id)
	}
	return client, err
}

// CreateClient creates a new client (admin only)
func (s *Service) CreateClient(ctx context.Context, command CreateClientCommand) (ClientView, error) {
	can, err := policyv1.Client.Create()
	if err != nil {
		return ClientView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return ClientView{}, err
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return ClientView{}, domainerr.Required("name")
	}

	client, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Client, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (Client, error) {
			client, err := store.InsertClient(ctx, name, command.Website)
			if err != nil {
				return Client{}, duplicateClientName(err)
			}
			return client, nil
		},
		Audit: func(client Client) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditClientCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientCreatedAuditRecord(metadata, client.ID)
			})
		},
		Relationships: func(client Client) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(policyv1.Client, client.ID, true)
		},
	})
	if err != nil {
		return ClientView{}, err
	}
	return ClientView{Client: client}, nil
}

// UpdateClient updates a client (admin only)
func (s *Service) UpdateClient(ctx context.Context, command UpdateClientCommand) (_ ClientView, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", command.ID))

	can, err := policyv1.Client.Edit(command.ID)
	if err != nil {
		return ClientView{}, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return ClientView{}, err
	}

	var client Client
	type update struct {
		client  Client
		changed []string
	}
	updated, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[update, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			client, err = lockClient(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (update, error) {
			stored, changed, err := updateLockedClient(ctx, store, client, command)
			return update{client: stored, changed: changed}, err
		},
		Audit: func(result update) mutation.Audit {
			if len(result.changed) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientMetadataUpdatedAuditRecord(metadata, client.ID, result.changed)
			})
		},
	})
	if err != nil {
		return ClientView{}, err
	}
	light, dark := s.clientLogos(ctx, updated.client.ID)
	return ClientView{Client: updated.client, LightLogo: light, DarkLogo: dark}, nil
}

// updateLockedClient applies command to the locked client and returns the
// client as stored and the changed fields.
func updateLockedClient(ctx context.Context, store Store, client Client, command UpdateClientCommand) (Client, []string, error) {
	var changes ClientChanges
	changedFields := make([]string, 0, 2)
	if command.Name != nil {
		name := strings.TrimSpace(*command.Name)
		if name == "" {
			return Client{}, nil, domainerr.Required("name")
		}
		if name != client.Name {
			changes.Name = &name
			changedFields = append(changedFields, "name")
		}
	}
	if command.Website != nil && (client.Website == nil || *command.Website != *client.Website) {
		changes.Website = command.Website
		changedFields = append(changedFields, "website")
	}
	if len(changedFields) == 0 {
		return client, nil, nil
	}
	stored, err := store.UpdateClient(ctx, client.ID, changes)
	if err != nil {
		return Client{}, nil, duplicateClientName(err)
	}
	return stored, changedFields, nil
}

// DeleteClient deletes a client (admin only)
func (s *Service) DeleteClient(ctx context.Context, command DeleteClientCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", command.ID))

	can, err := policyv1.Client.Delete(command.ID)
	if err != nil {
		return err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}

	var client Client
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[Client, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			client, err = lockClient(ctx, store, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (Client, error) {
			inUse, err := store.ClientInUse(ctx, client.ID)
			if err != nil {
				return Client{}, err
			}
			if inUse {
				return Client{}, domainerr.Precondition("client is still referenced")
			}
			if err := store.DeleteClient(ctx, client.ID); err != nil {
				return Client{}, err
			}
			return client, store.ReleaseAssets(ctx, AssetRelease{
				Owner:         AssetOwner{Type: "client", ID: client.ID},
				BindingPrefix: "logo",
			})
		},
		Audit: func(client Client) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditClientDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientDeletedAuditRecord(metadata, client.ID)
			})
		},
		Relationships: func(client Client) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(policyv1.Client, client.ID, false)
		},
	})
	return err
}
