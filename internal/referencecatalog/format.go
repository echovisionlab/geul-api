package referencecatalog

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

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

func (s *Service) ListFormats(ctx context.Context, query ReferenceListQuery) (Page[*managev1.Format], error) {
	return listReferences(ctx, s, formatKind, query, toProtoFormat)
}

func (s *Service) ListFormatsAdmin(ctx context.Context, query ReferenceListQuery) (Page[*managev1.FormatWithStats], error) {
	return listReferencesAdmin(ctx, s, formatKind, query, func(format *model.Format, usage int32) *managev1.FormatWithStats {
		return &managev1.FormatWithStats{Format: toProtoFormat(format), ReleaseCount: usage}
	})
}

func (s *Service) CreateFormat(ctx context.Context, command CreateReferenceCommand) (*managev1.Format, error) {
	format, err := formatKind.create(ctx, s, command)
	return projectReference(format, err, toProtoFormat)
}

func (s *Service) UpdateFormat(ctx context.Context, command UpdateReferenceCommand) (*managev1.Format, error) {
	format, err := formatKind.update(ctx, s, command)
	return projectReference(format, err, toProtoFormat)
}

func (s *Service) DeleteFormat(ctx context.Context, command DeleteReferenceCommand) error {
	return formatKind.delete(ctx, s, command)
}

func toProtoFormat(f *model.Format) *managev1.Format {
	return &managev1.Format{
		Id:   f.ID,
		Name: f.Name,
		Slug: f.Slug,
	}
}
