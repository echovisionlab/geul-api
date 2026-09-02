package referencecatalog

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// CreateClient creates a new client (admin only)
func (s *ClientService) CreateClient(
	ctx context.Context,
	req *connect.Request[managev1.CreateClientRequest],
) (*connect.Response[managev1.Client], error) {
	can, err := policyv1.Client.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	// Validate required fields
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, errs.Required("name")
	}

	client := model.Client{
		Name: name,
	}

	if req.Msg.Website != nil {
		client.Website = req.Msg.Website
	}

	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}}).Create(&client).Error; err != nil {
			if isUniqueViolation(err) {
				return errs.AlreadyExistsMsg("client with this name already exists")
			}
			return errs.Internal(err)
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditClientCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewClientCreatedAuditRecord(metadata, client.ID)
		}); err != nil {
			return err
		}
		policyTouch, err := policyv1.Client.TouchPolicy(client.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Client.DeletePolicy(client.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, errs.AlreadyExistsMsg("client with this name already exists")
		}
		return nil, err
	}

	return connect.NewResponse(s.toProtoClient(&client, nil)), nil
}

// UpdateClient updates a client (admin only)
func (s *ClientService) UpdateClient(
	ctx context.Context,
	req *connect.Request[managev1.UpdateClientRequest],
) (*connect.Response[managev1.Client], error) {
	can, err := policyv1.Client.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var client model.Client
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("client", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}

		updates := structured.Fields{}
		changedFields := make([]string, 0, 2)
		if req.Msg.Name != nil {
			name := strings.TrimSpace(*req.Msg.Name)
			if name == "" {
				return errs.Required("name")
			}
			if name != client.Name {
				updates["name"] = name
				changedFields = append(changedFields, "name")
			}
		}
		if req.Msg.Website != nil && (client.Website == nil || *req.Msg.Website != *client.Website) {
			updates["website"] = *req.Msg.Website
			changedFields = append(changedFields, "website")
		}
		if len(updates) == 0 {
			return nil
		}
		if err := s.applyClientUpdates(ctx, tx, &client, updates); err != nil {
			return err
		}
		if err := tx.First(&client, "id = ?", client.ID).Error; err != nil {
			return errs.Internal(err)
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewClientMetadataUpdatedAuditRecord(metadata, client.ID, changedFields)
		})
	}); err != nil {
		return nil, err
	}

	logoAssets := s.getClientLogoAssets(ctx, client.ID)
	return connect.NewResponse(s.toProtoClient(&client, logoAssets)), nil
}

func (s *ClientService) applyClientUpdates(ctx context.Context, db *gorm.DB, client *model.Client, updates structured.Fields) error {
	if len(updates) == 0 {
		return nil
	}
	if err := db.WithContext(ctx).Model(client).Updates(updates).Error; err != nil {
		if isUniqueViolation(err) {
			return errs.AlreadyExistsMsg("client with this name already exists")
		}
		return errs.Internal(err)
	}
	return nil
}

// DeleteClient deletes a client (admin only)
func (s *ClientService) DeleteClient(
	ctx context.Context,
	req *connect.Request[managev1.DeleteClientRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.Client.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var client model.Client
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&client, "id = ?", req.Msg.Id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("client", req.Msg.Id)
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		for _, relation := range []struct{ table string }{{"work_client"}, {"program_event_client"}} {
			var count int64
			if err := tx.Table(relation.table).Where("client_id = ?", client.ID).Limit(1).Count(&count).Error; err != nil {
				return errs.Internal(err)
			}
			if count > 0 {
				return errs.FailedPrecondition("client is still referenced")
			}
		}
		if err := tx.Delete(&client).Error; err != nil {
			return errs.Internal(err)
		}
		if err := s.assets.Release(ctx, tx, AssetRelease{
			Owner:         AssetOwner{Type: "client", ID: client.ID},
			BindingPrefix: "logo",
		}); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditClientDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewClientDeletedAuditRecord(metadata, client.ID)
		}); err != nil {
			return err
		}
		policyDelete, err := policyv1.Client.DeletePolicy(client.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Client.TouchPolicy(client.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyDelete}, []policyv1.RelationshipMutation{policyTouch})
	})
	if err != nil {
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}
