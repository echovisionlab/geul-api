package referencecatalog

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
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
	referenceService
}

func NewStyleService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *StyleService {
	return &StyleService{referenceService: newReferenceService(db, spiceDB)}
}

func NewAuditedStyleService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *StyleService {
	requireReferenceAuditWriter("style", auditWriter)
	service := NewStyleService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

var styleKind = referenceKind[model.Style]{
	resource:  "style",
	maxLength: referenceValueMaxLength,
	filters:   styleFilterConfig,
	sorts: &queryutil.SortConfig{
		AllowedFields: map[string]string{
			"name":       "name",
			"slug":       "slug",
			"created_at": "created_at",
		},
		DefaultSort: "name ASC",
	},
	policy: policyv1.Style,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditStyleCreated,
		updated:    sharedtelemetry.AuditStyleUpdated,
		deleted:    sharedtelemetry.AuditStyleDeleted,
		newCreated: sharedtelemetry.NewStyleCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewStyleMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewStyleDeletedAuditRecord,
	},
	relations: []referenceRelation{{table: "release_style", column: "style_id"}},
	newRecord: func(name, slug string, description *string) *model.Style {
		return &model.Style{Name: name, Slug: slug, Description: description, CreatedAt: dbtime.Now()}
	},
	values:      func(style *model.Style) (string, string, string) { return style.ID, style.Name, style.Slug },
	description: func(style *model.Style) *string { return style.Description },
}

func (s *StyleService) ListStyles(
	ctx context.Context,
	req *connect.Request[managev1.ListStylesRequest],
) (*connect.Response[managev1.ListStylesResponse], error) {
	styles, total, page, err := styleKind.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ListStylesResponse{
		Styles: mapReferences(styles, toProtoStyle), Pagination: page.BuildResponse(total),
	}), nil
}

func (s *StyleService) ListStylesAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListStylesAdminRequest],
) (*connect.Response[managev1.ListStylesAdminResponse], error) {
	styles, usage, total, page, err := styleKind.listAdmin(ctx, s.referenceService, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	result := mapReferences(styles, func(style *model.Style) *managev1.StyleWithStats {
		return &managev1.StyleWithStats{Style: toProtoStyle(style), ReleaseCount: usage[style.ID]}
	})
	return connect.NewResponse(&managev1.ListStylesAdminResponse{
		Styles: result, Pagination: page.BuildResponse(total),
	}), nil
}

func (s *StyleService) CreateStyle(
	ctx context.Context,
	req *connect.Request[managev1.CreateStyleRequest],
) (*connect.Response[managev1.Style], error) {
	style, err := styleKind.createAudited(ctx, s.referenceService, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoStyle(style)), nil
}

func (s *StyleService) UpdateStyle(
	ctx context.Context,
	req *connect.Request[managev1.UpdateStyleRequest],
) (*connect.Response[managev1.Style], error) {
	style, err := styleKind.updateAudited(ctx, s.referenceService, req.Msg.Id, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoStyle(style)), nil
}

func (s *StyleService) DeleteStyle(
	ctx context.Context,
	req *connect.Request[managev1.DeleteStyleRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := styleKind.deleteAudited(ctx, s.referenceService, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func toProtoStyle(style *model.Style) *managev1.Style {
	return &managev1.Style{
		Id: style.ID, Name: style.Name, Slug: style.Slug, Description: style.Description, CreatedAt: timestamppb.New(style.CreatedAt),
	}
}
