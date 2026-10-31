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

func (s *Service) ListStyles(ctx context.Context, query ReferenceListQuery) (Page[*managev1.Style], error) {
	return listReferences(ctx, s, styleKind, query, toProtoStyle)
}

func (s *Service) ListStylesAdmin(ctx context.Context, query ReferenceListQuery) (Page[*managev1.StyleWithStats], error) {
	return listReferencesAdmin(ctx, s, styleKind, query, func(style *model.Style, usage int32) *managev1.StyleWithStats {
		return &managev1.StyleWithStats{Style: toProtoStyle(style), ReleaseCount: usage}
	})
}

func (s *Service) CreateStyle(ctx context.Context, command CreateReferenceCommand) (*managev1.Style, error) {
	style, err := styleKind.create(ctx, s, command)
	return projectReference(style, err, toProtoStyle)
}

func (s *Service) UpdateStyle(ctx context.Context, command UpdateReferenceCommand) (*managev1.Style, error) {
	style, err := styleKind.update(ctx, s, command)
	return projectReference(style, err, toProtoStyle)
}

func (s *Service) DeleteStyle(ctx context.Context, command DeleteReferenceCommand) error {
	return styleKind.delete(ctx, s, command)
}

func toProtoStyle(style *model.Style) *managev1.Style {
	return &managev1.Style{
		Id: style.ID, Name: style.Name, Slug: style.Slug, Description: style.Description, CreatedAt: timestamppb.New(style.CreatedAt),
	}
}
