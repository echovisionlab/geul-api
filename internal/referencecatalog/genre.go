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

type GenreService struct {
	managev1connect.UnimplementedGenreServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

func NewGenreService(db *gorm.DB, spiceDB *auth.SpiceDBClient) *GenreService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &GenreService{db: db, spiceDB: spiceDB}
}

func NewAuditedGenreService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient) *GenreService {
	if auditWriter == nil {
		panic("genre audit writer is required")
	}
	service := NewGenreService(db, spiceDB)
	service.auditWriter = auditWriter
	return service
}

var genreSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"slug":       "slug",
		"created_at": "created_at",
	},
	DefaultSort: "name ASC",
}

var genreCRUD = referenceCRUD[model.Genre]{
	resource: "genre",
	filters:  genreFilterConfig,
	sorts:    &genreSortConfig,
	newRecord: func(name, slug string, description *string) *model.Genre {
		return &model.Genre{Name: name, Slug: slug, Description: description, CreatedAt: time.Now()}
	},
	values: func(genre *model.Genre) (string, string) {
		return genre.Name, genre.Slug
	},
	description: func(genre *model.Genre) *string { return genre.Description },
}

func (s *GenreService) ListGenres(
	ctx context.Context,
	req *connect.Request[managev1.ListGenresRequest],
) (*connect.Response[managev1.ListGenresResponse], error) {
	genres, total, page, err := genreCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	result := make([]*managev1.Genre, len(genres))
	for i := range genres {
		result[i] = toProtoGenre(&genres[i])
	}
	return connect.NewResponse(&managev1.ListGenresResponse{
		Genres: result, Pagination: page.BuildResponse(total),
	}), nil
}

func (s *GenreService) ListGenresAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListGenresAdminRequest],
) (*connect.Response[managev1.ListGenresAdminResponse], error) {
	can, err := policyv1.Genre.List()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAdminCan(ctx, s.spiceDB, can); err != nil {
		return nil, err
	}
	genres, total, page, err := genreCRUD.list(ctx, s.db, req.Msg.Filters, req.Msg.Sorts, req.Msg.Pagination)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(genres))
	for i := range genres {
		ids[i] = genres[i].ID
	}
	counts, err := loadReferenceCounts(ctx, s.db, "release_genre", "genre_id", ids)
	if err != nil {
		return nil, err
	}
	result := make([]*managev1.GenreWithStats, len(genres))
	for i := range genres {
		result[i] = &managev1.GenreWithStats{Genre: toProtoGenre(&genres[i]), ReleaseCount: counts[genres[i].ID]}
	}
	return connect.NewResponse(&managev1.ListGenresAdminResponse{
		Genres: result, Pagination: page.BuildResponse(total),
	}), nil
}

func (s *GenreService) CreateGenre(
	ctx context.Context,
	req *connect.Request[managev1.CreateGenreRequest],
) (*connect.Response[managev1.Genre], error) {
	can, err := policyv1.Genre.Create()
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var genre *model.Genre
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var err error
		genre, err = genreCRUD.create(ctx, tx, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
		if err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditGenreCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewGenreCreatedAuditRecord(metadata, genre.ID)
		}); err != nil {
			return err
		}
		policyTouch, err := policyv1.Genre.TouchPolicy(genre.ID)
		if err != nil {
			return err
		}
		policyDelete, err := policyv1.Genre.DeletePolicy(genre.ID)
		if err != nil {
			return err
		}
		return write([]policyv1.RelationshipMutation{policyTouch}, []policyv1.RelationshipMutation{policyDelete})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoGenre(genre)), nil
}

func (s *GenreService) UpdateGenre(
	ctx context.Context,
	req *connect.Request[managev1.UpdateGenreRequest],
) (*connect.Response[managev1.Genre], error) {
	can, err := policyv1.Genre.Edit(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var genre *model.Genre
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		genre, err = genreCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var changedFields []string
		genre, changedFields, err = genreCRUD.updateLocked(ctx, tx, req.Msg.Id, genre, req.Msg.Name, req.Msg.Slug, req.Msg.Description)
		if err != nil || len(changedFields) == 0 {
			return err
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditGenreUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewGenreMetadataUpdatedAuditRecord(metadata, genre.ID, changedFields)
		})
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toProtoGenre(genre)), nil
}

func (s *GenreService) DeleteGenre(
	ctx context.Context,
	req *connect.Request[managev1.DeleteGenreRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.Genre.Delete(req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	var deleted *model.Genre
	_, err = authzmutation.Execute(ctx, s.db, s.spiceDB, func(tx *gorm.DB, write authzmutation.WriteRelationships) error {
		var err error
		deleted, err = genreCRUD.lockForMutation(ctx, tx, req.Msg.Id)
		if err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := genreCRUD.deleteLockedWithRelationGuards(ctx, tx, req.Msg.Id, deleted, referenceRelationGuard{table: "release_genre", column: "genre_id"}); err != nil {
			return err
		}
		if err := domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditGenreDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewGenreDeletedAuditRecord(metadata, deleted.ID)
		}); err != nil {
			return err
		}
		policyDelete, err := policyv1.Genre.DeletePolicy(deleted.ID)
		if err != nil {
			return err
		}
		policyTouch, err := policyv1.Genre.TouchPolicy(deleted.ID)
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

func toProtoGenre(genre *model.Genre) *managev1.Genre {
	result := &managev1.Genre{
		Id: genre.ID, Name: genre.Name, Slug: genre.Slug, CreatedAt: timestamppb.New(genre.CreatedAt),
	}
	result.Description = genre.Description
	return result
}
