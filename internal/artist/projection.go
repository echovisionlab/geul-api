package artist

import (
	"context"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/pathslug"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// View is an Artist as the manage API renders it: the row, its
// source-locale name and content document, its ready OG image and its
// ordered images.
type View struct {
	Artist   Artist
	Name     string
	Document *contentv1.RichTextDocument
	Revision string
	OgImage  *mediaasset.Reference
	Images   []ArtistImageProjection
}

func artistView(artist Artist, document artistDocument, ogAsset *mediaasset.Reference, images []ArtistImageProjection) View {
	return View{Artist: artist, Name: document.Name, Document: document.Document, Revision: document.Revision, OgImage: ogAsset, Images: images}
}

// checkSlugAvailable validates slug and requires that no other Artist holds
// it; excludeID is the Artist being updated, empty for a new one.
func checkSlugAvailable(ctx context.Context, store Store, slug string, excludeID string) error {
	if err := pathslug.Validate(slug); err != nil {
		return err
	}
	if slug == "" {
		return nil
	}
	taken, err := store.SlugTaken(ctx, slug, excludeID)
	if err != nil {
		return err
	}
	if taken {
		return domainerr.Duplicate("artist", "slug", slug)
	}
	return nil
}

// CheckArtistSlugAvailableQuery asks whether Slug is free for a new Artist,
// or for the Artist ExcludeArtistID when it is set.
type CheckArtistSlugAvailableQuery struct {
	Slug            string
	ExcludeArtistID *string
}

// CheckArtistSlugAvailable reports whether no other Artist and no route use
// the slug. Checking for an existing Artist requires Edit on it; for a new
// one, the platform-wide create action.
func (s *Service) CheckArtistSlugAvailable(ctx context.Context, query CheckArtistSlugAvailableQuery) (bool, error) {
	existing := query.ExcludeArtistID != nil && *query.ExcludeArtistID != ""
	excludeID := ""
	if existing {
		if err := artistEntity.RequirePermission(ctx, s.spiceDB, *query.ExcludeArtistID, policyv1.Artist.Edit); err != nil {
			return false, err
		}
		excludeID = *query.ExcludeArtistID
	} else if err := artistEntity.RequireCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	var available bool
	err := s.units.Read(ctx, func(store Store) error {
		taken, err := store.SlugTaken(ctx, query.Slug, excludeID)
		if err != nil || taken {
			return err
		}
		available, err = store.ResourceRouteAvailable(ctx, query.Slug)
		return err
	})
	return available, err
}

// getArtistLabelIDs returns the Artist's Labels in link order. Draft Labels
// are among them, because their people hold authority over the Artist too.
func (s *Service) getArtistLabelIDs(ctx context.Context, artistID string) ([]string, error) {
	var labelIDs []string
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		labelIDs, err = store.LabelIDsInLinkOrder(ctx, artistID)
		return err
	})
	return labelIDs, err
}

// artistDocument is an Artist's source-locale name, content document and
// OG image.
type artistDocument struct {
	Name      string
	Document  *contentv1.RichTextDocument
	Revision  string
	OgAssetID *string
}

// overlayArtistSourceLocaleDocument rereads the Artist row into artist and
// returns its source-locale document from the same snapshot.
func (s *Service) overlayArtistSourceLocaleDocument(ctx context.Context, artist *Artist) (artistDocument, error) {
	artistID := artist.ID
	projection, err := s.loadCreativeManageContentProjection(ctx, artistID, func(ctx context.Context, store Store) error {
		current, err := store.ShareLockArtist(ctx, artistID)
		if err != nil {
			return err
		}
		*artist = current
		return nil
	})
	if err != nil {
		return artistDocument{}, err
	}
	return artistDocument{
		Name: projection.SourceTitle, Document: projection.Document, Revision: projection.Revision,
		OgAssetID: projection.SourceOgAssetID,
	}, nil
}

func (s *Service) overlayArtistSourceLocaleDocuments(ctx context.Context, artists []Artist) ([]artistDocument, error) {
	documents := make([]artistDocument, len(artists))
	for i := range artists {
		var err error
		if documents[i], err = s.overlayArtistSourceLocaleDocument(ctx, &artists[i]); err != nil {
			return nil, err
		}
	}
	return documents, nil
}

func (s *Service) loadReadyArtistOgAssets(ctx context.Context, documents []artistDocument) (map[string]*mediaasset.Reference, error) {
	candidates := make([]*string, 0, len(documents))
	for i := range documents {
		candidates = append(candidates, documents[i].OgAssetID)
	}
	return s.loadReadyManageOgAssetRefs(ctx, candidates...)
}

func (s *Service) artistWithReadyOg(ctx context.Context, artist Artist, document artistDocument) (View, error) {
	ogAsset, err := s.readyManageOgAssetRef(ctx, document.OgAssetID)
	if err != nil {
		return View{}, err
	}
	images, err := s.loadArtistImages(ctx, artist.ID)
	if err != nil {
		return View{}, err
	}
	return artistView(artist, document, ogAsset, images), nil
}

func (s *Service) loadReadyManageOgAssetRefs(ctx context.Context, candidates ...*string) (map[string]*mediaasset.Reference, error) {
	ids := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate != nil {
			id := strings.TrimSpace(*candidate)
			if id != "" {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					ids = append(ids, id)
				}
			}
		}
	}
	var ready map[string]*mediaasset.Reference
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		ready, err = store.ReadyAssets(ctx, ids)
		return err
	})
	return ready, err
}

func manageOgAssetFromReadyMap(ready map[string]*mediaasset.Reference, candidates ...*string) *mediaasset.Reference {
	for _, candidate := range candidates {
		if candidate != nil {
			if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
				return asset
			}
		}
	}
	return nil
}

func (s *Service) readyManageOgAssetRef(ctx context.Context, candidates ...*string) (*mediaasset.Reference, error) {
	ready, err := s.loadReadyManageOgAssetRefs(ctx, candidates...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidates...), nil
}
