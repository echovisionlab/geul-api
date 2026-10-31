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

type GenreService struct {
	managev1connect.UnimplementedGenreServiceHandler
	referenceService
}

func NewGenreService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *GenreService {
	return &GenreService{referenceService: newReferenceService(db, spiceDB)}
}

func NewAuditedGenreService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *GenreService {
	requireReferenceAuditWriter("genre", auditWriter)
	service := NewGenreService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

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

func (s *GenreService) ListGenres(
	ctx context.Context,
	req *connect.Request[managev1.ListGenresRequest],
) (*connect.Response[managev1.ListGenresResponse], error) {
	genres, total, page, err := genreKind.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ListGenresResponse{
		Genres: mapReferences(genres, toProtoGenre), Pagination: page.BuildResponse(total),
	}), nil
}

func (s *GenreService) ListGenresAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListGenresAdminRequest],
) (*connect.Response[managev1.ListGenresAdminResponse], error) {
	genres, usage, total, page, err := genreKind.listAdmin(ctx, s.referenceService, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	result := mapReferences(genres, func(genre *model.Genre) *managev1.GenreWithStats {
		return &managev1.GenreWithStats{Genre: toProtoGenre(genre), ReleaseCount: usage[genre.ID]}
	})
	return connect.NewResponse(&managev1.ListGenresAdminResponse{
		Genres: result, Pagination: page.BuildResponse(total),
	}), nil
}

func (s *GenreService) CreateGenre(
	ctx context.Context,
	req *connect.Request[managev1.CreateGenreRequest],
) (*connect.Response[managev1.Genre], error) {
	genre, err := genreKind.createAudited(ctx, s.referenceService, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoGenre(genre)), nil
}

func (s *GenreService) UpdateGenre(
	ctx context.Context,
	req *connect.Request[managev1.UpdateGenreRequest],
) (*connect.Response[managev1.Genre], error) {
	genre, err := genreKind.updateAudited(ctx, s.referenceService, req.Msg.Id, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoGenre(genre)), nil
}

func (s *GenreService) DeleteGenre(
	ctx context.Context,
	req *connect.Request[managev1.DeleteGenreRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if err := genreKind.deleteAudited(ctx, s.referenceService, req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func toProtoGenre(genre *model.Genre) *managev1.Genre {
	return &managev1.Genre{
		Id: genre.ID, Name: genre.Name, Slug: genre.Slug, Description: genre.Description, CreatedAt: timestamppb.New(genre.CreatedAt),
	}
}
