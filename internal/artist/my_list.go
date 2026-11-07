package artist

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var errNotAuthenticated = domainerr.New(domainerr.Unauthenticated, "", "not authenticated")

// OwnedDocumentIDs is the Artists the caller may manage, which are those it may view.
func (s *Service) OwnedDocumentIDs(ctx context.Context) ([]string, error) {
	user := auth.GetUser(ctx)
	if user == nil {
		return nil, errNotAuthenticated
	}
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	if err != nil {
		return nil, errNotAuthenticated.WithCause(err)
	}
	actor, err := policyv1.NewAccountIdentityActor(subject.ID.String())
	if err != nil {
		return nil, errNotAuthenticated.WithCause(err)
	}
	artistIDs, err := s.spiceDB.LookupResources(ctx, policyv1.Artist.LookupManage(), actor)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	return artistIDs, nil
}

// ListMyArtists returns one page of the Artists the caller may manage.
func (s *Service) ListMyArtists(ctx context.Context, query ListArtistsQuery) (ArtistPage, error) {
	artistIDs, err := s.OwnedDocumentIDs(ctx)
	if err != nil {
		return ArtistPage{}, err
	}
	if len(artistIDs) == 0 {
		return ArtistPage{Artists: []View{}, Pagination: Pagination{Limit: 20}}, nil
	}
	artists, err := s.listArtists(ctx, ListPage{IDs: artistIDs}, query)
	if err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{Artists: artists.project(), Pagination: artists.pagination}, nil
}
