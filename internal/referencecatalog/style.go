package referencecatalog

import (
	"context"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var styleKind = referenceKind{
	kind:      KindStyle,
	maxLength: referenceValueMaxLength,
	policy:    policyv1.Style,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditStyleCreated,
		updated:    sharedtelemetry.AuditStyleUpdated,
		deleted:    sharedtelemetry.AuditStyleDeleted,
		newCreated: sharedtelemetry.NewStyleCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewStyleMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewStyleDeletedAuditRecord,
	},
	described: true,
}

func (s *Service) ListStyles(ctx context.Context, query ReferenceListQuery) (Page[Reference], error) {
	return styleKind.list(ctx, s, query)
}

func (s *Service) ListStylesAdmin(ctx context.Context, query ReferenceListQuery) (Page[ReferenceUsage], error) {
	return styleKind.listAdmin(ctx, s, query)
}

func (s *Service) CreateStyle(ctx context.Context, command CreateReferenceCommand) (Reference, error) {
	return styleKind.create(ctx, s, command)
}

func (s *Service) UpdateStyle(ctx context.Context, command UpdateReferenceCommand) (Reference, error) {
	return styleKind.update(ctx, s, command)
}

func (s *Service) DeleteStyle(ctx context.Context, command DeleteReferenceCommand) error {
	return styleKind.delete(ctx, s, command)
}
