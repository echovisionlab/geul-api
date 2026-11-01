package referencecatalog

import (
	"context"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var genreKind = referenceKind{
	kind:      KindGenre,
	maxLength: referenceValueMaxLength,
	policy:    policyv1.Genre,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditGenreCreated,
		updated:    sharedtelemetry.AuditGenreUpdated,
		deleted:    sharedtelemetry.AuditGenreDeleted,
		newCreated: sharedtelemetry.NewGenreCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewGenreMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewGenreDeletedAuditRecord,
	},
	described: true,
}

func (s *Service) ListGenres(ctx context.Context, query ReferenceListQuery) (Page[Reference], error) {
	return genreKind.list(ctx, s, query)
}

func (s *Service) ListGenresAdmin(ctx context.Context, query ReferenceListQuery) (Page[ReferenceUsage], error) {
	return genreKind.listAdmin(ctx, s, query)
}

func (s *Service) CreateGenre(ctx context.Context, command CreateReferenceCommand) (Reference, error) {
	return genreKind.create(ctx, s, command)
}

func (s *Service) UpdateGenre(ctx context.Context, command UpdateReferenceCommand) (Reference, error) {
	return genreKind.update(ctx, s, command)
}

func (s *Service) DeleteGenre(ctx context.Context, command DeleteReferenceCommand) error {
	return genreKind.delete(ctx, s, command)
}
