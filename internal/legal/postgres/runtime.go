package legalpostgres

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *store) LockActivation(ctx context.Context, kind legal.PolicyKind) error {
	return s.ports.Routes.LockActivation(ctx, s.tx, string(kind))
}

func (s *store) CurrentRoute(ctx context.Context, kind legal.PolicyKind) (*legal.CurrentRoute, error) {
	return s.ports.Routes.CurrentForRoute(ctx, s.tx, string(kind))
}

func (s *store) RequestOG(ctx context.Context, kind legal.PolicyKind, id, locale string, allLocales bool, reason string) error {
	return s.ports.Routes.RequestSaved(ctx, s.tx, string(kind), id, locale, allLocales, reason)
}

func (s *store) ReleaseRoute(ctx context.Context, kind legal.PolicyKind) error {
	return s.ports.Routes.CancelAndRelease(ctx, s.tx, string(kind), s.ports.Routes.RouteID(string(kind)))
}

// NoticeRun maps a Campaign delivery run to the notice run Legal reads.
func NoticeRun(run model.CampaignDeliveryRun) legal.NoticeRun {
	return legal.NoticeRun{
		ID: run.ID, RunKind: run.RunKind, TemplateEventKey: run.TemplateEventKey,
		TermsID: run.TermsID, PrivacyID: run.PrivacyID, TemplateData: run.TemplateData,
	}
}

func (s *store) SealNoticeRun(ctx context.Context, kind legal.PolicyKind, id, eventKey string, templateData map[string]string, scheduledAt time.Time) (*legal.NoticeRun, error) {
	run, err := s.ports.Notices.CreateRun(ctx, s.tx, string(kind), id, eventKey, templateData, scheduledAt)
	if err != nil || run == nil {
		return nil, err
	}
	sealed := NoticeRun(*run)
	return &sealed, nil
}

func (s *store) LatestSealedVersion(ctx context.Context, kind legal.PolicyKind) (int, error) {
	return s.ports.Notices.LatestSealedVersion(ctx, s.tx, string(kind))
}

func (s *store) CancelScheduledNotices(ctx context.Context, kind legal.PolicyKind, id string) error {
	return s.ports.Notices.CancelScheduledRuns(ctx, s.tx, string(kind), id)
}

func (s *store) StopNoticesOfDeletedVersion(ctx context.Context, kind legal.PolicyKind, id string) error {
	return s.ports.Notices.StopRunsForDeletedPolicy(ctx, s.tx, string(kind), id)
}

func (s *store) UpdateNoticeRuns(ctx context.Context, kind legal.PolicyKind, id string) ([]legal.NoticeRun, error) {
	runs, err := s.ports.Notices.UpdateNoticeRuns(ctx, s.tx, string(kind), id)
	if err != nil {
		return nil, err
	}
	mapped := make([]legal.NoticeRun, len(runs))
	for i, run := range runs {
		mapped[i] = NoticeRun(run)
	}
	return mapped, nil
}

func (s *store) CreateAutomaticPreview(ctx context.Context, preview legal.AutomaticPreview) error {
	return s.ports.PreviewLinks.CreateAutomaticPreview(ctx, s.tx, preview)
}

func (s *store) DeleteAutomaticPreview(ctx context.Context, token, entityType, entityID string) error {
	return s.ports.PreviewLinks.DeleteAutomaticPreview(ctx, s.tx, token, entityType, entityID)
}

func (s *store) DeletePreviewLinks(ctx context.Context, entityType, entityID string) error {
	return s.ports.PreviewLinks.DeleteForEntity(ctx, s.tx, entityType, entityID)
}

func (s *store) ResolveSessionPrincipal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.tx, sessionID)
}

// collaborationResourceType names the kind's collaboration rooms.
func collaborationResourceType(kind legal.PolicyKind) intrav1.CollaborationResourceType {
	if kind == legal.Privacy {
		return intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_PRIVACY_HISTORY
	}
	return intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_TERMS_HISTORY
}

func (s *store) RequireCurrentContributors(ctx context.Context, kind legal.PolicyKind, id string, contributors []string) error {
	return s.ports.Checkpoints.RequireCurrentContributors(ctx, s.tx, collaborationResourceType(kind), id, contributors)
}

func (s *store) PreferredLocale(ctx context.Context, memberID string) (*string, error) {
	return mutation.BestEffortRead(ctx, s.tx, func(tx *gorm.DB) (*string, error) {
		return s.ports.Members.PreferredLocale(ctx, tx, memberID)
	})
}

func (s *store) TranslationSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	return mutation.BestEffortRead(ctx, s.tx, func(tx *gorm.DB) (translation.RuntimeSettings, error) {
		return translation.LoadRuntimeSettings(ctx, tx)
	})
}

func (s *store) PublishContentUpdate(ctx context.Context, update legal.ContentUpdate) error {
	entity := managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS
	if update.Kind == legal.Privacy {
		entity = managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY
	}
	return contentsignal.PublishInteractive(ctx, s.tx, entity, update.PolicyID, update.Locale, update.DocumentRevision, update.TargetRevision, true, update.Deleted, update.ContributorMemberID)
}
