package referencecatalog

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type StyleService struct {
	managev1connect.UnimplementedStyleServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

func NewAuditedStyleService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *StyleService {
	if auditWriter == nil {
		panic("style audit writer is required")
	}
	service := NewStyleService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func NewStyleService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *StyleService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &StyleService{db: db, spiceDB: spiceDB}
}

var styleSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"slug":       "slug",
		"created_at": "created_at",
	},
	DefaultSort: "name ASC",
}

var styleCRUD = referenceCRUD[model.Style]{
	resource: "style",
	filters:  styleFilterConfig,
	sorts:    &styleSortConfig,
	newRecord: func(name, slug string, description *string) *model.Style {
		return &model.Style{Name: name, Slug: slug, Description: description, CreatedAt: time.Now()}
	},
	values: func(style *model.Style) (string, string) {
		return style.Name, style.Slug
	},
	description: func(style *model.Style) *string { return style.Description },
}

func (s *StyleService) ListStyles(
	ctx context.Context,
	req *connect.Request[managev1.ListStylesRequest],
) (_ *connect.Response[managev1.ListStylesResponse], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := errs.OperationLogLevel(returnErr)
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.ListStyles", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	styles, total, page, err := styleCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	result := make([]*managev1.Style, len(styles))
	for i := range styles {
		result[i] = toProtoStyle(&styles[i])
	}
	return connect.NewResponse(&managev1.ListStylesResponse{
		Styles: result, Pagination: page.BuildResponse(total),
	}), nil
}

func (s *StyleService) ListStylesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListStylesAdminRequest],
) (_ *connect.Response[managev1.ListStylesAdminResponse], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := errs.OperationLogLevel(returnErr)
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.ListStylesAdmin", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	can, err := policyv1.Style.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	styles, total, page, err := styleCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(styles))
	for i := range styles {
		ids[i] = styles[i].ID
	}
	counts, err := loadReferenceCounts(ctx, s.db, "release_style", "style_id", ids)
	if err != nil {
		return nil, err
	}
	result := make([]*managev1.StyleWithStats, len(styles))
	for i := range styles {
		result[i] = &managev1.StyleWithStats{Style: toProtoStyle(&styles[i]), ReleaseCount: counts[styles[i].ID]}
	}
	return connect.NewResponse(&managev1.ListStylesAdminResponse{
		Styles: result, Pagination: page.BuildResponse(total),
	}), nil
}

func (s *StyleService) CreateStyle(
	ctx context.Context,
	req *connect.Request[managev1.CreateStyleRequest],
) (_ *connect.Response[managev1.Style], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := errs.OperationLogLevel(returnErr)
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.CreateStyle", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	can, err := policyv1.Style.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var style *model.Style
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var err error
		style, err = styleCRUD.create(ctx, tx, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
		if err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditStyleCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewStyleCreatedAuditRecord(metadata, style.ID)
		}); err != nil {
			return err
		}
		policyTouch, err := policyv1.Style.TouchPolicy(style.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Style.DeletePolicy(style.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoStyle(style)), nil
}

func (s *StyleService) UpdateStyle(
	ctx context.Context,
	req *connect.Request[managev1.UpdateStyleRequest],
) (_ *connect.Response[managev1.Style], returnErr error) {
	defer func() {
		if returnErr == nil || req == nil {
			return
		}
		level := errs.OperationLogLevel(returnErr)
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.UpdateStyle", "outcome", "failed", "style_id", req.Msg.GetId(), failurecause.Attr(returnErr))
	}()

	can, err := policyv1.Style.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var style *model.Style
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		style, err = styleCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var changedFields []string
		style, changedFields, err = styleCRUD.updateLocked(ctx, tx, req.Msg.Id, style, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
		if err != nil || len(changedFields) == 0 {
			return err
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditStyleUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewStyleMetadataUpdatedAuditRecord(metadata, style.ID, changedFields)
		})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoStyle(style)), nil
}

func (s *StyleService) DeleteStyle(
	ctx context.Context,
	req *connect.Request[managev1.DeleteStyleRequest],
) (_ *connect.Response[managev1.DeleteResponse], returnErr error) {
	defer func() {
		if returnErr == nil || req == nil {
			return
		}
		level := errs.OperationLogLevel(returnErr)
		slog.Log(ctx, level, "content operation failed", "domain", "referencecatalog", "operation", "referencecatalog.DeleteStyle", "outcome", "failed", "style_id", req.Msg.GetId(), failurecause.Attr(returnErr))
	}()

	can, err := policyv1.Style.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var deleted *model.Style
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var err error
		deleted, err = styleCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := styleCRUD.deleteLockedWithRelationGuards(ctx, tx, req.Msg.Id, deleted, referenceRelationGuard{table: "release_style", column: "style_id"}); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditStyleDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewStyleDeletedAuditRecord(metadata, deleted.ID)
		}); err != nil {
			return err
		}
		policyDelete, err := policyv1.Style.DeletePolicy(deleted.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Style.TouchPolicy(deleted.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyDelete}, []policyv1.RelationshipMutation{policyTouch})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func toProtoStyle(style *model.Style) *managev1.Style {
	result := &managev1.Style{
		Id: style.ID, Name: style.Name, Slug: style.Slug, CreatedAt: timestamppb.New(style.CreatedAt),
	}
	result.Description = style.Description
	return result
}
