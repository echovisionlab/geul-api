package form

import (
	"connectrpc.com/connect"
	"context"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func (s *FormService) ListFormSubmissions(
	ctx context.Context,
	req *connect.Request[managev1.ListFormSubmissionsRequest],
) (*connect.Response[managev1.ListFormSubmissionsResponse], error) {
	// Keep behavior consistent with other submission endpoints:
	// unknown form IDs should return not_found (including admins).
	var formCount int64
	if err := s.db.WithContext(ctx).
		Model(&model.Form{}).
		Where("id = ?", req.Msg.FormId).
		Count(&formCount).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if formCount == 0 {
		return nil, errs.NotFound("form", req.Msg.FormId)
	}
	if err := s.requireFormAction(ctx, req.Msg.FormId, formActionManage); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFound("form", req.Msg.FormId)
		}
		return nil, err
	}

	var submissions []model.FormSubmission
	var total int64

	query := s.db.WithContext(ctx).Model(&model.FormSubmission{}).Where("form_id = ?", req.Msg.FormId)

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}

	// Apply pagination
	limit := int32(50)
	offset := int32(0)
	if req.Msg.Pagination != nil {
		if req.Msg.Pagination.Limit > 0 {
			limit = req.Msg.Pagination.Limit
		}
		offset = req.Msg.Pagination.Offset
	}

	if err := query.
		Order("created_at DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&submissions).Error; err != nil {
		return nil, errs.Internal(err)
	}

	protoSubmissions := make([]*managev1.FormSubmission, len(submissions))
	for i := range submissions {
		protoSubmissions[i] = s.toProtoSubmission(&submissions[i])
	}
	if s.securityAccess != nil {
		if err := s.securityAccess.AppendFormSubmissions(ctx, req.Msg.FormId); err != nil {
			return nil, securityAccessUnavailable()
		}
	}

	return connect.NewResponse(&managev1.ListFormSubmissionsResponse{
		Submissions: protoSubmissions,
		Pagination: &commonv1.PaginationResponse{
			Total:   int32(total),
			Limit:   limit,
			Offset:  offset,
			HasMore: offset+limit < int32(total),
		},
	}), nil
}

// GetFormSubmissionStats returns aggregate submission stats for a form (admin only).
func (s *FormService) GetFormSubmissionWithSchema(
	ctx context.Context,
	req *connect.Request[managev1.GetFormSubmissionRequest],
) (*connect.Response[managev1.FormSubmissionWithSchema], error) {
	var submission model.FormSubmission
	if err := s.db.WithContext(ctx).First(&submission, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("submission not found")
		}
		return nil, errs.Internal(err)
	}

	// Get the form to include schema
	var form model.Form
	if err := s.db.WithContext(ctx).First(&form, "id = ?", submission.FormID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("form not found")
		}
		return nil, errs.Internal(err)
	}
	if err := s.requireFormAction(ctx, form.ID, formActionManage); err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFoundMsg("submission not found")
		}
		return nil, err
	}

	formSchema, sourceTitle, err := loadFormSourceSchema(ctx, s.db, form.ID)
	if err != nil {
		return nil, err
	}
	if s.securityAccess != nil {
		if err := s.securityAccess.AppendFormSubmission(ctx, submission.ID); err != nil {
			return nil, securityAccessUnavailable()
		}
	}

	return connect.NewResponse(&managev1.FormSubmissionWithSchema{
		Submission: s.toProtoSubmission(&submission),
		FormSchema: formSchema,
		FormTitle:  resolveFormTitle(sourceTitle),
	}), nil
}

// DeleteFormSubmission deletes a submission (admin only)
