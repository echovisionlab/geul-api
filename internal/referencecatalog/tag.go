package referencecatalog

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

func (s *Service) ListTags(ctx context.Context, query ReferenceListQuery) (Page[*managev1.Tag], error) {
	return listReferences(ctx, s, tagKind, query, toProtoTag)
}

func (s *Service) ListTagsAdmin(ctx context.Context, query ReferenceListQuery) (Page[*managev1.TagWithStats], error) {
	return listReferencesAdmin(ctx, s, tagKind, query, func(tag *model.Tag, usage int32) *managev1.TagWithStats {
		return &managev1.TagWithStats{Tag: toProtoTag(tag), PostCount: usage}
	})
}

func (s *Service) CreateTag(ctx context.Context, command CreateReferenceCommand) (*managev1.Tag, error) {
	tag, err := tagKind.create(ctx, s, command)
	return projectReference(tag, err, toProtoTag)
}

func (s *Service) UpdateTag(ctx context.Context, command UpdateReferenceCommand) (*managev1.Tag, error) {
	tag, err := tagKind.update(ctx, s, command)
	return projectReference(tag, err, toProtoTag)
}

func (s *Service) DeleteTag(ctx context.Context, command DeleteReferenceCommand) error {
	return tagKind.delete(ctx, s, command)
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
