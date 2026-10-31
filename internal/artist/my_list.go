package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var errNotAuthenticated = domainerr.New(domainerr.Unauthenticated, "", "not authenticated")

// ListMyArtists returns one page of the Artists the caller may manage.
func (s *Service) ListMyArtists(ctx context.Context, query ListArtistsQuery) (ArtistPage, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return ArtistPage{}, errNotAuthenticated
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return ArtistPage{}, errNotAuthenticated.WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return ArtistPage{}, errNotAuthenticated.WithCause(err)
	}
	artistIDs, err := s.spiceDB.LookupResources(ctx, policyv1.Artist.LookupManage(), actor)
	if err != nil {
		return ArtistPage{}, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if len(artistIDs) == 0 {
		return ArtistPage{Artists: []*managev1.Artist{}, Pagination: &commonv1.PaginationResponse{Limit: 20}}, nil
	}
	artists, err := s.listArtists(ctx, s.db.WithContext(ctx).Model(&model.Artist{}).Where("id IN ?", artistIDs), query, false)
	if err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{Artists: artists.project(), Pagination: artists.pagination}, nil
}
