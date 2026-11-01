package form

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// DeleteFormSubmissionCommand deletes one submission.
type DeleteFormSubmissionCommand struct {
	ID string
}

// DeleteFormSubmission deletes a submission of a Form the caller manages.
func (s *Service) DeleteFormSubmission(ctx context.Context, command DeleteFormSubmissionCommand) error {
	notFound := domainerr.New(domainerr.NotFound, "", "submission not found")
	var submission Submission
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		submission, err = store.FindSubmission(ctx, command.ID)
		return err
	}); err != nil {
		if errors.Is(err, ErrSubmissionNotFound) {
			return notFound
		}
		return err
	}
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[string, Store]{
		Lock: func(ctx context.Context, store Store) error {
			_, err := lockFormRoot(ctx, store, submission.FormID, notFound)
			return err
		},
		Access: s.freshFormAction(submission.FormID, formActionManage),
		Change: func(ctx context.Context, store Store) (string, error) {
			deleted, err := store.DeleteSubmission(ctx, submission.ID)
			if err != nil {
				return "", err
			}
			if !deleted {
				return "", notFound
			}
			return submission.ID, nil
		},
		Audit: formSubmissionDeletedAudit,
	})
	return err
}
