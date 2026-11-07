package artist

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// SetArtistParentCommand moves an Artist under ParentArtistID, or to the top
// when it is nil.
type SetArtistParentCommand struct {
	ArtistID       string
	ParentArtistID *string
}

// SetArtistParent moves the Artist; see entityparticipant.Mutations.SetParent
// for who may.
func (s *Service) SetArtistParent(ctx context.Context, command SetArtistParentCommand) (_ View, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("artist_id", command.ArtistID))

	parent, err := artistEntity.CanonicalParent(command.ParentArtistID)
	if err != nil {
		return View{}, err
	}
	var previous *string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		previous, err = artistEntity.LoadParent(ctx, store, command.ArtistID)
		return err
	}); err != nil {
		return View{}, err
	}
	if err := s.participants.SetParent(ctx, entityparticipant.ParentChange[Store]{
		EntityID: command.ArtistID, Previous: previous, Parent: parent,
		Write: func(ctx context.Context, store Store, parent *string) error {
			change := MetadataChange{MetadataChange: entityparticipant.MetadataChange{Parent: entityparticipant.Assignment{Changed: true, Value: parent}}}
			return store.UpdateDocumentMetadata(ctx, command.ArtistID, change, time.Now().UTC())
		},
		Signal: func(ctx context.Context, store Store) error {
			return store.PublishContentUpdate(ctx, ContentUpdate{ArtistID: command.ArtistID, Kind: StateUpdate, StatePaths: []string{"parent_artist_id"}})
		},
	}); err != nil {
		return View{}, err
	}
	data, err := s.GetArtistEditorData(ctx, GetArtistEditorDataQuery{ID: command.ArtistID})
	return data.Artist, err
}
