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

var genreKind = referenceKind[model.Genre]{
	resource:  "genre",
	maxLength: referenceValueMaxLength,
	filters:   genreFilterConfig,
	sorts: &queryutil.SortConfig{
		AllowedFields: map[string]string{
			"name":       "name",
			"slug":       "slug",
			"created_at": "created_at",
		},
		DefaultSort: "name ASC",
	},
	policy: policyv1.Genre,
	audit: referenceAudit{
		created:    sharedtelemetry.AuditGenreCreated,
		updated:    sharedtelemetry.AuditGenreUpdated,
		deleted:    sharedtelemetry.AuditGenreDeleted,
		newCreated: sharedtelemetry.NewGenreCreatedAuditRecord,
		newUpdated: sharedtelemetry.NewGenreMetadataUpdatedAuditRecord,
		newDeleted: sharedtelemetry.NewGenreDeletedAuditRecord,
	},
	relations: []referenceRelation{{table: "release_genre", column: "genre_id"}},
	newRecord: func(name, slug string, description *string) *model.Genre {
		return &model.Genre{Name: name, Slug: slug, Description: description, CreatedAt: dbtime.Now()}
	},
	values:      func(genre *model.Genre) (string, string, string) { return genre.ID, genre.Name, genre.Slug },
	description: func(genre *model.Genre) *string { return genre.Description },
}

func (s *Service) ListGenres(ctx context.Context, query ReferenceListQuery) (Page[*managev1.Genre], error) {
	return listReferences(ctx, s, genreKind, query, toProtoGenre)
}

func (s *Service) ListGenresAdmin(ctx context.Context, query ReferenceListQuery) (Page[*managev1.GenreWithStats], error) {
	return listReferencesAdmin(ctx, s, genreKind, query, func(genre *model.Genre, usage int32) *managev1.GenreWithStats {
		return &managev1.GenreWithStats{Genre: toProtoGenre(genre), ReleaseCount: usage}
	})
}

func (s *Service) CreateGenre(ctx context.Context, command CreateReferenceCommand) (*managev1.Genre, error) {
	genre, err := genreKind.create(ctx, s, command)
	return projectReference(genre, err, toProtoGenre)
}

func (s *Service) UpdateGenre(ctx context.Context, command UpdateReferenceCommand) (*managev1.Genre, error) {
	genre, err := genreKind.update(ctx, s, command)
	return projectReference(genre, err, toProtoGenre)
}

func (s *Service) DeleteGenre(ctx context.Context, command DeleteReferenceCommand) error {
	return genreKind.delete(ctx, s, command)
}

func toProtoGenre(genre *model.Genre) *managev1.Genre {
	return &managev1.Genre{
		Id: genre.ID, Name: genre.Name, Slug: genre.Slug, Description: genre.Description, CreatedAt: timestamppb.New(genre.CreatedAt),
	}
}
