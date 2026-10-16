package form

import (
	"connectrpc.com/connect"
	"context"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log/slog"
)

func (s *FormService) DeleteFormSubmission(
	ctx context.Context,
	req *connect.Request[managev1.DeleteFormSubmissionRequest],
) (rpcResponse *connect.Response[managev1.DeleteResponse], rpcErr error) {
	defer func() {
		if rpcErr == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(rpcErr) {
		case connect.CodeInternal, connect.CodeUnknown, connect.CodeUnavailable, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		slog.Log(ctx, level, "RPC failed", "domain", "form", "event", "form.rpc_failed", "operation", "DeleteFormSubmission", "outcome", "failed", "reason", connect.CodeOf(rpcErr).String(), failurecause.Attr(rpcErr))
	}()

	var submission model.FormSubmission
	if err := s.db.WithContext(ctx).First(&submission, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("submission not found")
		}
		return nil, errs.Internal(err)
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var form model.Form
		if err := tx.WithContext(ctx).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id").
			First(&form, "id = ?", submission.FormID).Error; err != nil {
			return err
		}
		if err := s.requireFreshFormAction(ctx, tx, submission.FormID, formActionManage); err != nil {
			return err
		}
		result := tx.Delete(&model.FormSubmission{}, "id = ?", submission.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errs.NotFoundMsg("submission not found")
		}
		return s.appendFormSubmissionDeletedAudit(ctx, tx, submission.ID)
	}); err != nil {
		return nil, errs.Wrap(err)
	}

	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

// Helper methods
