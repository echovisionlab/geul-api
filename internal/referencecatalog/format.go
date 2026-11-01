package referencecatalog

import (
	"context"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// formatKind has no description or creation time, and its listing order
// breaks name ties by ID.
var formatKind = referenceKind{
	kind:      KindFormat,
	maxLength: shortReferenceValueMaxLength,
	policy:    policyv1.Format,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditFormatCreated,
		updated:    sharedtelemetry.AuditFormatUpdated,
		deleted:    sharedtelemetry.AuditFormatDeleted,
		newCreated: sharedtelemetry.NewFormatCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewFormatMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewFormatDeletedAuditRecord,
	},
}

func (s *Service) ListFormats(ctx context.Context, query ReferenceListQuery) (Page[Reference], error) {
	return formatKind.list(ctx, s, query)
}

func (s *Service) ListFormatsAdmin(ctx context.Context, query ReferenceListQuery) (Page[ReferenceUsage], error) {
	return formatKind.listAdmin(ctx, s, query)
}

func (s *Service) CreateFormat(ctx context.Context, command CreateReferenceCommand) (Reference, error) {
	return formatKind.create(ctx, s, command)
}

func (s *Service) UpdateFormat(ctx context.Context, command UpdateReferenceCommand) (Reference, error) {
	return formatKind.update(ctx, s, command)
}

func (s *Service) DeleteFormat(ctx context.Context, command DeleteReferenceCommand) error {
	return formatKind.delete(ctx, s, command)
}
