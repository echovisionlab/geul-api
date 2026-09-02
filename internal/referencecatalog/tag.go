package referencecatalog

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// tagSortConfig defines allowed sort fields for tags
var tagSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"slug":       "slug",
		"created_at": "created_at",
	},
	DefaultSort: "name ASC",
}

var tagListCRUD = referenceCRUD[model.Tag]{
	resource: "tag",
	filters:  tagFilterConfig,
	sorts:    &tagSortConfig,
	newRecord: func(name, slug string, _ *string) *model.Tag {
		return &model.Tag{Name: name, Slug: slug, CreatedAt: time.Now()}
	},
	values: func(tag *model.Tag) (string, string) {
		return tag.Name, tag.Slug
	},
}

// TagService implements the TagService Connect handler
type TagService struct {
	managev1connect.UnimplementedTagServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
	menuTargets MenuTargets
}

func NewAuditedTagService(db *gorm.DB, auditWriter domainaudit.Appender, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *TagService {
	if auditWriter == nil {
		panic("tag audit writer is required")
	}
	service := NewTagService(db, menuTargets, spiceDB)
	service.auditWriter = auditWriter
	return service
}

// NewTagService creates a new TagService
func NewTagService(db *gorm.DB, menuTargets MenuTargets, spiceDB *auth.SpiceDBClient) *TagService {
	if db == nil {
		panic("db is required")
	}
	if menuTargets == nil {
		panic("menu targets are required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &TagService{db: db, spiceDB: spiceDB, menuTargets: menuTargets}
}

// GetTag retrieves a tag by ID
// GetTagBySlug retrieves a tag by slug
// ListTags returns a paginated list of tags
func (s *TagService) ListTags(
	ctx context.Context,
	req *connect.Request[managev1.ListTagsRequest],
) (*connect.Response[managev1.ListTagsResponse], error) {
	tags, total, page, err := tagListCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	protoTags := make([]*managev1.Tag, len(tags))
	for i := range tags {
		protoTags[i] = toProtoTag(&tags[i])
	}

	return connect.NewResponse(&managev1.ListTagsResponse{
		Tags:       protoTags,
		Pagination: page.BuildResponse(total),
	}), nil
}

// ListTagsAdmin returns a paginated list of tags with stats (admin only)
func (s *TagService) ListTagsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListTagsAdminRequest],
) (*connect.Response[managev1.ListTagsAdminResponse], error) {
	can, err := policyv1.Tag.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}

	tags, total, page, err := tagListCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	tagIDs := make([]string, len(tags))
	for i, t := range tags {
		tagIDs[i] = t.ID
	}

	postCounts, err := loadReferenceCounts(ctx, s.db, "post_tag", "tag_id", tagIDs)
	if err != nil {
		return nil, err
	}

	// Build response with stats
	tagsWithStats := make([]*managev1.TagWithStats, len(tags))
	for i := range tags {
		tagsWithStats[i] = &managev1.TagWithStats{
			Tag:       toProtoTag(&tags[i]),
			PostCount: postCounts[tags[i].ID],
		}
	}

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
	can, err := policyv1.Tag.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var tag *model.Tag
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var err error
		tag, err = tagListCRUD.create(ctx, tx, req.Msg.Name, req.Msg.Slug, nil)
		if err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewTagCreatedAuditRecord(metadata, tag.ID)
		}); err != nil {
			return err
		}
		policyTouch, err := policyv1.Tag.TouchPolicy(tag.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Tag.DeletePolicy(tag.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
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
	can, err := policyv1.Tag.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var tag *model.Tag
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		tag, err = tagListCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		previousSlug := tag.Slug
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var changedFields []string
		tag, changedFields, err = tagListCRUD.updateLocked(ctx, tx, req.Msg.Id, tag, req.Msg.Name, req.Msg.Slug, nil)
		if err != nil || len(changedFields) == 0 {
			return err
		}
		if req.Msg.Slug != nil && previousSlug != tag.Slug {
			if err := s.menuTargets.UpdateSlug(ctx, tx, MenuTargetSlugChange{
				Target:   MenuTarget{LinkType: "tag", ID: tag.ID, Slug: previousSlug},
				NextSlug: tag.Slug,
			}); err != nil {
				return err
			}
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTagUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewTagMetadataUpdatedAuditRecord(metadata, tag.ID, changedFields)
		})
	}); err != nil {
		return nil, err
	}

	return connect.NewResponse(toProtoTag(tag)), nil
}

// DeleteTag deletes a tag (admin only)
func (s *TagService) DeleteTag(
	ctx context.Context,
	req *connect.Request[managev1.DeleteTagRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.Tag.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	var tag *model.Tag
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var err error
		tag, err = tagListCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := tagListCRUD.deleteLockedWithRelationGuards(ctx, tx, req.Msg.Id, tag, referenceRelationGuard{table: "post_tag", column: "tag_id"}); err != nil {
			return err
		}
		if err := s.menuTargets.Remove(ctx, tx, MenuTarget{LinkType: "tag", ID: tag.ID, Slug: tag.Slug}); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditTagDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewTagDeletedAuditRecord(metadata, tag.ID)
		}); err != nil {
			return err
		}
		policyDelete, err := policyv1.Tag.DeletePolicy(tag.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Tag.TouchPolicy(tag.ID)
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

// ==================== Helper Methods ====================

// toProtoTag converts a model.Tag to protobuf Tag
func toProtoTag(t *model.Tag) *managev1.Tag {
	tag := &managev1.Tag{
		Id:        t.ID,
		Name:      t.Name,
		Slug:      &t.Slug, // Slug is NOT NULL in DB
		CreatedAt: timestamppb.New(t.CreatedAt),
	}
	// Note: tag table has no updated_at column

	return tag
}
