package referencecatalog

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// FormatService implements the FormatService Connect handler
type FormatService struct {
	managev1connect.UnimplementedFormatServiceHandler
	referenceService
}

func NewAuditedFormatService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *FormatService {
	requireReferenceAuditWriter("format", auditWriter)
	service := NewFormatService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

// NewFormatService creates a new FormatService
func NewFormatService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *FormatService {
	return &FormatService{referenceService: newReferenceService(db, spiceDB)}
}

// formatKind has no description or creation time, and its listing order
// breaks name ties by ID.
var formatKind = referenceKind[model.Format]{
	resource:  "format",
	maxLength: shortReferenceValueMaxLength,
	filters:   formatFilterConfig,
	sorts: &queryutil.SortConfig{
		AllowedFields: map[string]string{
			"name": "name",
			"slug": "slug",
		},
		DefaultSort: "name ASC, id ASC",
	},
	policy: policyv1.Format,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditFormatCreated,
		updated:    sharedtelemetry.AuditFormatUpdated,
		deleted:    sharedtelemetry.AuditFormatDeleted,
		newCreated: sharedtelemetry.NewFormatCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewFormatMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewFormatDeletedAuditRecord,
	},
	relations: []referenceRelation{{table: "release_format", column: "format_id"}},
	newRecord: func(name, slug string, _ *string) *model.Format {
		return &model.Format{Name: name, Slug: slug}
	},
	values: func(format *model.Format) (string, string, string) { return format.ID, format.Name, format.Slug },
}

// ListFormats returns a paginated list of formats
func (s *FormatService) ListFormats(
	ctx context.Context,
	req *connect.Request[managev1.ListFormatsRequest],
) (*connect.Response[managev1.ListFormatsResponse], error) {
	formats, total, page, err := formatKind.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ListFormatsResponse{
		Formats:    mapReferences(formats, toProtoFormat),
		Pagination: page.BuildResponse(total),
	}), nil
}

// ListFormatsAdmin returns a paginated list of formats with stats
func (s *FormatService) ListFormatsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListFormatsAdminRequest],
) (*connect.Response[managev1.ListFormatsAdminResponse], error) {
	formats, usage, total, page, err := formatKind.listAdmin(ctx, s.referenceService, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	formatsWithStats := mapReferences(formats, func(format *model.Format) *managev1.FormatWithStats {
		return &managev1.FormatWithStats{Format: toProtoFormat(format), ReleaseCount: usage[format.ID]}
	})
	return connect.NewResponse(&managev1.ListFormatsAdminResponse{
		Formats:    formatsWithStats,
		Pagination: page.BuildResponse(total),
	}), nil
}

// CreateFormat creates a new format (admin only)
func (s *FormatService) CreateFormat(
	ctx context.Context,
	req *connect.Request[managev1.CreateFormatRequest],
) (*connect.Response[managev1.Format], error) {
	format, err := formatKind.createAudited(ctx, s.referenceService, req.Msg.Name, req.Msg.Slug, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoFormat(format)), nil
}

// UpdateFormat updates an existing format (admin only)
func (s *FormatService) UpdateFormat(
	ctx context.Context,
	req *connect.Request[managev1.UpdateFormatRequest],
) (*connect.Response[managev1.Format], error) {
	format, err := formatKind.updateAudited(ctx, s.referenceService, req.Msg.Id, req.Msg.Name, req.Msg.Slug, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoFormat(format)), nil
}

// DeleteFormat deletes a format (admin only)
func (s *FormatService) DeleteFormat(
	ctx context.Context,
	req *connect.Request[managev1.DeleteFormatRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := formatKind.deleteAudited(ctx, s.referenceService, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func toProtoFormat(f *model.Format) *managev1.Format {
	return &managev1.Format{
		Id:   f.ID,
		Name: f.Name,
		Slug: f.Slug,
	}
}
