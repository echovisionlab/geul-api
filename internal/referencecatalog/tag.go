package referencecatalog

import (
	"context"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// tagKind has no description, and menu items can link to it.
var tagKind = referenceKind{
	kind:      KindTag,
	maxLength: shortReferenceValueMaxLength,
	policy:    policyv1.Tag,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditTagCreated,
		updated:    sharedtelemetry.AuditTagUpdated,
		deleted:    sharedtelemetry.AuditTagDeleted,
		newCreated: sharedtelemetry.NewTagCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewTagMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewTagDeletedAuditRecord,
	},
	menuLinkType: "tag",
}

func (s *Service) ListTags(ctx context.Context, query ReferenceListQuery) (Page[Reference], error) {
	return tagKind.list(ctx, s, query)
}

func (s *Service) ListTagsAdmin(ctx context.Context, query ReferenceListQuery) (Page[ReferenceUsage], error) {
	return tagKind.listAdmin(ctx, s, query)
}

func (s *Service) CreateTag(ctx context.Context, command CreateReferenceCommand) (Reference, error) {
	return tagKind.create(ctx, s, command)
}

func (s *Service) UpdateTag(ctx context.Context, command UpdateReferenceCommand) (Reference, error) {
	return tagKind.update(ctx, s, command)
}

func (s *Service) DeleteTag(ctx context.Context, command DeleteReferenceCommand) error {
	return tagKind.delete(ctx, s, command)
}
