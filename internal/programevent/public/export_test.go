package public

import (
	"context"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// LoadCredits projects the public credits of an event as the public event
// read renders them.
func LoadCredits(ctx context.Context, service *Service, eventID string) ([]*openv1.ProgramEventCredit, error) {
	var credits []Credit
	err := service.units.Read(ctx, func(store Store) error {
		var err error
		credits, err = service.loadPublicProgramEventCredits(ctx, store, eventID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return eventMessage(EventDetail{Credits: credits}).Credits, nil
}
