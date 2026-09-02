package form

import (
	"context"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func (s *FormService) toProtoForm(ctx context.Context, f *model.Form) (*managev1.Form, error) {
	sourceState, err := LoadFormCanonicalSourceDocumentState(ctx, s.db, f.ID, f.SourceLocale)
	if err != nil {
		return nil, err
	}
	ogAsset, err := s.og.ReadyAsset(ctx, s.db, sourceState.OgAssetID, f.OgAssetID)
	if err != nil {
		return nil, err
	}
	form := &managev1.Form{
		Id:           f.ID,
		Title:        resolveFormTitle(sourceState.Title),
		Schema:       sourceState.ContentJSON,
		Status:       managev1.FormStatus(managev1.FormStatus_value[string(f.Status)]),
		IsPublic:     f.IsPublic,
		AllowedRoles: f.AllowedRoles,
		CreatedAt:    timestamppb.New(f.CreatedAt),
		OgAsset:      ogAsset,
	}

	if f.Slug != nil {
		form.Slug = f.Slug
	}
	if f.RequireAuth != nil {
		form.RequireAuth = f.RequireAuth
	}
	if f.AllowDuplicateSubmission != nil {
		form.AllowDuplicateSubmission = f.AllowDuplicateSubmission
	}
	if f.MaxSubmissions != nil {
		form.MaxSubmissions = f.MaxSubmissions
	}
	if f.OpensAt != nil {
		form.OpensAt = timestamppb.New(*f.OpensAt)
	}
	if f.ClosesAt != nil {
		form.ClosesAt = timestamppb.New(*f.ClosesAt)
	}
	if f.UpdatedAt != nil {
		form.UpdatedAt = timestamppb.New(*f.UpdatedAt)
	}
	if imageAsset := s.getFormFeaturedImageAsset(ctx, f.ID); imageAsset != nil {
		form.FeaturedImageAsset = imageAsset
	}
	// HasPassword indicates if form is password protected (don't expose the hash)
	form.HasPassword = f.AccessPassword != nil && *f.AccessPassword != ""

	return form, nil
}

func loadFormSubmissionCounts(ctx context.Context, db *gorm.DB, formIDs []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(formIDs))
	if len(formIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		FormID string `gorm:"column:form_id"`
		Count  int64  `gorm:"column:submission_count"`
	}
	if err := db.WithContext(ctx).
		Table("form_submission").
		Select("form_id, COUNT(*) AS submission_count").
		Where("form_id IN ?", formIDs).
		Group("form_id").
		Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	for _, row := range rows {
		counts[row.FormID] = row.Count
	}
	return counts, nil
}

func toProtoFormSummaryWithSubmissionCount(
	f *model.Form,
	sourceTitle string,
	submissionCount int64,
) *managev1.FormSummary {
	summary := &managev1.FormSummary{
		Id:              f.ID,
		Title:           resolveFormTitle(&sourceTitle),
		Status:          managev1.FormStatus(managev1.FormStatus_value[string(f.Status)]),
		SubmissionCount: int32(submissionCount),
		CreatedAt:       timestamppb.New(f.CreatedAt),
	}

	if f.Slug != nil {
		summary.Slug = f.Slug
	}
	if f.UpdatedAt != nil {
		summary.UpdatedAt = timestamppb.New(*f.UpdatedAt)
	}

	return summary
}

func (s *FormService) toProtoSubmission(sub *model.FormSubmission) *managev1.FormSubmission {
	submission := &managev1.FormSubmission{
		Id:        sub.ID,
		FormId:    sub.FormID,
		Data:      sub.Data,
		CreatedAt: timestamppb.New(sub.CreatedAt),
	}

	if sub.MemberID != nil {
		submission.MemberId = sub.MemberID
	}
	if sub.IPAddress != nil {
		submission.IpAddress = sub.IPAddress
	}
	if sub.UserAgent != nil {
		submission.UserAgent = sub.UserAgent
	}

	return submission
}

func (s *FormService) getFormFeaturedImageAsset(ctx context.Context, formID string) *commonv1.AssetRef {
	return s.assets.FeaturedImage(ctx, s.db, formID)
}
