package application

import (
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toProtoTranslationLocale(locale localization.RuntimeLocale) *managev1.TranslationLocale {
	resp := &managev1.TranslationLocale{
		Code:                      locale.Code,
		DisplayName:               locale.DisplayName,
		Enabled:                   locale.Enabled,
		IsPublic:                  locale.IsPublic,
		Dir:                       locale.Dir,
		MachineTranslationAllowed: locale.MachineTranslationAllowed,
		SortOrder:                 locale.SortOrder,
	}
	if locale.FontProfile != nil {
		resp.FontProfile = locale.FontProfile
	}
	return resp
}

func toProtoTranslationJob(job model.TranslationJob) *managev1.TranslationJob {
	definition, _ := translation.DefinitionForKind(job.EntityType)
	resp := &managev1.TranslationJob{
		Id:                    job.ID,
		Target:                &managev1.TranslationTarget{EntityType: definition.Proto, EntityId: job.EntityID},
		TargetLocale:          job.TargetLocale,
		SourceLocale:          job.SourceLocale,
		RequestArtifactDigest: job.RequestArtifactDigest,
		Status:                toProtoTranslationJobStatus(job.Status),
		OperationId:           job.OperationID,
		RequestedAt:           timestamppb.New(job.RequestedAt),
	}
	resp.RequestedByMemberId = job.RequestedByMemberID
	if job.Provider != nil {
		resp.Provider = job.Provider
	}
	if job.Model != nil {
		resp.Model = job.Model
	}
	if job.StartedAt != nil {
		resp.StartedAt = timestamppb.New(*job.StartedAt)
	}
	return resp
}

func toProtoTranslationJobStatus(status string) managev1.TranslationJobStatus {
	switch status {
	case translationJobStatusQueued:
		return managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_QUEUED
	case translationJobStatusRunning:
		return managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING
	default:
		return managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_UNSPECIFIED
	}
}

func generateUUID() string {
	return uuid.NewString()
}
