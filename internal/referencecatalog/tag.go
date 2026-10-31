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

// tagKind has no description, and menu items can link to it.
var tagKind = referenceKind[model.Tag]{
	resource:  "tag",
	maxLength: shortReferenceValueMaxLength,
	filters:   tagFilterConfig,
	sorts: &queryutil.SortConfig{
		AllowedFields: map[string]string{
			"name":       "name",
			"slug":       "slug",
			"created_at": "created_at",
		},
		DefaultSort: "name ASC",
	},
	policy: policyv1.Tag,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditTagCreated,
		updated:    sharedtelemetry.AuditTagUpdated,
		deleted:    sharedtelemetry.AuditTagDeleted,
		newCreated: sharedtelemetry.NewTagCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewTagMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewTagDeletedAuditRecord,
	},
	relations:    []referenceRelation{{table: "post_tag", column: "tag_id"}},
	menuLinkType: "tag",
	newRecord: func(name, slug string, _ *string) *model.Tag {
		return &model.Tag{Name: name, Slug: slug, CreatedAt: dbtime.Now()}
	},
	values: func(tag *model.Tag) (string, string, string) { return tag.ID, tag.Name, tag.Slug },
}

// TagService implements the TagService Connect handler
type TagService struct {
	managev1connect.UnimplementedTagServiceHandler
	referenceService
}

func NewAuditedTagService(db *gorm.DB, auditWriter domainaudit.Appender, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *TagService {
	requireReferenceAuditWriter("tag", auditWriter)
	service := NewTagService(db, menuTargets, spiceDB)
	service.auditWriter = auditWriter
	return service
}

// NewTagService creates a new TagService
func NewTagService(db *gorm.DB, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *TagService {
	return &TagService{referenceService: newMenuTargetReferenceService(db, menuTargets, spiceDB)}
}

// ListTags returns a paginated list of tags
func (s *TagService) ListTags(
	ctx context.Context,
	req *connect.Request[managev1.ListTagsRequest],
) (*connect.Response[managev1.ListTagsResponse], error) {
	tags, total, page, err := tagKind.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ListTagsResponse{
		Tags:       mapReferences(tags, toProtoTag),
		Pagination: page.BuildResponse(total),
	}), nil
}

// ListTagsAdmin returns a paginated list of tags with stats (admin only)
func (s *TagService) ListTagsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListTagsAdminRequest],
) (*connect.Response[managev1.ListTagsAdminResponse], error) {
	tags, usage, total, page, err := tagKind.listAdmin(ctx, s.referenceService, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	tagsWithStats := mapReferences(tags, func(tag *model.Tag) *managev1.TagWithStats {
		return &managev1.TagWithStats{Tag: toProtoTag(tag), PostCount: usage[tag.ID]}
	})
	return connect.NewResponse(&managev1.ListTagsAdminResponse{
		Tags:       tagsWithStats,
		Pagination: page.BuildResponse(total),
	}), nil
}

// CreateTag creates a new tag (admin only)
func (s *TagService) CreateTag(
	ctx context.Context,
	req *connect.Request[managev1.CreateTagRequest],
) (*connect.Response[managev1.Tag], error) {
	tag, err := tagKind.createAudited(ctx, s.referenceService, req.Msg.Name, req.Msg.Slug, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoTag(tag)), nil
}

// UpdateTag updates an existing tag (admin only)
func (s *TagService) UpdateTag(
	ctx context.Context,
	req *connect.Request[managev1.UpdateTagRequest],
) (*connect.Response[managev1.Tag], error) {
	tag, err := tagKind.updateAudited(ctx, s.referenceService, req.Msg.Id, req.Msg.Name, req.Msg.Slug, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoTag(tag)), nil
}

// DeleteTag deletes a tag (admin only)
func (s *TagService) DeleteTag(
	ctx context.Context,
	req *connect.Request[managev1.DeleteTagRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := tagKind.deleteAudited(ctx, s.referenceService, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// toProtoTag maps a tag; the tag table has no updated_at column.
func toProtoTag(t *model.Tag) *managev1.Tag {
	return &managev1.Tag{
		Id:        t.ID,
		Name:      t.Name,
		Slug:      &t.Slug,
		CreatedAt: timestamppb.New(t.CreatedAt),
	}
}
