package label

import (
	"context"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// SetLabelParentCommand moves a Label under ParentLabelID, or to the top when
// it is nil.
type SetLabelParentCommand struct {
	LabelID       string
	ParentLabelID *string
}

// SetLabelParent moves the Label; see entityparticipant.Mutations.SetParent for
// who may.
func (s *Service) SetLabelParent(ctx context.Context, command SetLabelParentCommand) (_ View, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	parent, err := labelEntity.CanonicalParent(command.ParentLabelID)
	if err != nil {
		return View{}, err
	}
	var previous *string
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		previous, err = labelEntity.LoadParent(ctx, store, command.LabelID)
		return err
	}); err != nil {
		return View{}, err
	}
	if err := s.participants.SetParent(ctx, entityparticipant.ParentChange[Store]{
		EntityID: command.LabelID, Previous: previous, Parent: parent,
		Write: func(ctx context.Context, store Store, parent *string) error {
			change := entityparticipant.MetadataChange{Parent: entityparticipant.Assignment{Changed: true, Value: parent}}
			return store.UpdateDocumentMetadata(ctx, command.LabelID, change, time.Now().UTC())
		},
		Signal: func(ctx context.Context, store Store) error {
			return store.PublishContentUpdate(ctx, ContentUpdate{LabelID: command.LabelID, Kind: StateUpdate})
		},
	}); err != nil {
		return View{}, err
	}
	data, err := s.GetLabelEditorData(ctx, GetLabelEditorDataQuery{ID: command.LabelID})
	return data.Label, err
}
