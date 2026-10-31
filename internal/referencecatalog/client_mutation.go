package referencecatalog

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
	if dberrors.IsUniqueViolation(err) {
		return domainerr.New(domainerr.AlreadyExists, "name", "client with this name already exists").WithCause(err)
	}
	return err
}

// lockClient takes the Client row for a mutation.
func lockClient(ctx context.Context, tx *gorm.DB, id string) (*model.Client, error) {
	var client model.Client
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&client, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("client", id)
		}
		return nil, err
	}
	return &client, nil
}

// CreateClient creates a new client (admin only)
func (s *Service) CreateClient(ctx context.Context, command CreateClientCommand) (*managev1.Client, error) {
	can, err := policyv1.Client.Create()
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(command.Name)
	if name == "" {
		return nil, domainerr.Required("name")
	}

	client, err := mutation.Run(ctx, s.mutations, mutation.Spec[*model.Client]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (*model.Client, error) {
			client := &model.Client{Name: name, Website: command.Website}
			if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}}).Create(client).Error; err != nil {
				return nil, duplicateClientName(err)
			}
			return client, nil
		},
		Audit: func(client *model.Client) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditClientCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientCreatedAuditRecord(metadata, client.ID)
			})
		},
		Relationships: func(client *model.Client) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(policyv1.Client, client.ID, true)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.toProtoClient(client, nil), nil
}

// UpdateClient updates a client (admin only)
func (s *Service) UpdateClient(ctx context.Context, command UpdateClientCommand) (_ *managev1.Client, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", command.ID))

	can, err := policyv1.Client.Edit(command.ID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var client *model.Client
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[[]string]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			client, err = lockClient(ctx, tx, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) ([]string, error) {
			return updateLockedClient(ctx, tx, client, command)
		},
		Audit: func(changedFields []string) mutation.Audit {
			if len(changedFields) == 0 {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientMetadataUpdatedAuditRecord(metadata, client.ID, changedFields)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return s.toProtoClient(client, s.getClientLogoAssets(ctx, client.ID)), nil
}

// updateLockedClient applies command to the locked client, rereads it, and
// returns the changed fields.
func updateLockedClient(ctx context.Context, tx *gorm.DB, client *model.Client, command UpdateClientCommand) ([]string, error) {
	updates := structured.Fields{}
	changedFields := make([]string, 0, 2)
	if command.Name != nil {
		name := strings.TrimSpace(*command.Name)
		if name == "" {
			return nil, domainerr.Required("name")
		}
		if name != client.Name {
			updates["name"] = name
			changedFields = append(changedFields, "name")
		}
	}
	if command.Website != nil && (client.Website == nil || *command.Website != *client.Website) {
		updates["website"] = *command.Website
		changedFields = append(changedFields, "website")
	}
	if len(updates) == 0 {
		return nil, nil
	}
	if err := tx.WithContext(ctx).Model(client).Updates(updates).Error; err != nil {
		return nil, duplicateClientName(err)
	}
	if err := tx.First(client, "id = ?", client.ID).Error; err != nil {
		return nil, err
	}
	return changedFields, nil
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

	var client *model.Client
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[*model.Client]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			client, err = lockClient(ctx, tx, command.ID)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (*model.Client, error) {
			for _, relation := range []struct{ table string }{{"work_client"}, {"program_event_client"}} {
				var count int64
				if err := tx.Table(relation.table).Where("client_id = ?", client.ID).Limit(1).Count(&count).Error; err != nil {
					return nil, err
				}
				if count > 0 {
					return nil, domainerr.Precondition("client is still referenced")
				}
			}
			if err := tx.Delete(client).Error; err != nil {
				return nil, err
			}
			return client, s.assets.Release(ctx, tx, AssetRelease{
				Owner:         AssetOwner{Type: "client", ID: client.ID},
				BindingPrefix: "logo",
			})
		},
		Audit: func(client *model.Client) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditClientDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientDeletedAuditRecord(metadata, client.ID)
			})
		},
		Relationships: func(client *model.Client) (apply, compensate []policyv1.RelationshipMutation, err error) {
			return referencePolicyRelationships(policyv1.Client, client.ID, false)
		},
	})
	return err
}
