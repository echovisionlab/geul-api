package form

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// DeleteFormSubmissionCommand deletes one submission.
type DeleteFormSubmissionCommand struct {
	ID string
}

// DeleteFormSubmission deletes a submission of a Form the caller manages.
func (s *Service) DeleteFormSubmission(ctx context.Context, command DeleteFormSubmissionCommand) error {
	notFound := domainerr.New(domainerr.NotFound, "", "submission not found")
	var submission model.FormSubmission
	if err := s.db.WithContext(ctx).First(&submission, "id = ?", command.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return notFound
		}
		return err
	}
	var form model.Form
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[string]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockFormRoot(ctx, tx, submission.FormID, &form, notFound, "id")
		},
		Access: s.freshFormAction(submission.FormID, formActionManage),
		Change: func(ctx context.Context, tx *gorm.DB) (string, error) {
			result := tx.WithContext(ctx).Delete(&model.FormSubmission{}, "id = ?", submission.ID)
			if result.Error != nil {
				return "", result.Error
			}
			if result.RowsAffected == 0 {
				return "", notFound
			}
			return submission.ID, nil
		},
		Audit: formSubmissionDeletedAudit,
	})
	return err
}
