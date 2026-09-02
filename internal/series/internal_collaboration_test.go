package series

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/collaboration"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postSeriesCollaborationAudit struct{ records []sharedtelemetry.AuditRecord }

func (a *postSeriesCollaborationAudit) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	a.records = append(a.records, record)
	return nil
}

var _ domainaudit.Appender = (*postSeriesCollaborationAudit)(nil)

type postSeriesCollaborationContributorResolver struct {
	subject auth.AccountIdentitySubject
}

func (r postSeriesCollaborationContributorResolver) ResolveActiveSubjects(
	_ context.Context,
	_ *gorm.DB,
	members []string,
) (map[string]auth.AccountIdentitySubject, error) {
	result := make(map[string]auth.AccountIdentitySubject, len(members))
	for _, member := range members {
		result[member] = r.subject
	}
	return result, nil
}

type postSeriesCollaborationAuthorizer struct{}

func (postSeriesCollaborationAuthorizer) Authorize(
	context.Context,
	string,
	intrav1.CollaborationPermission,
	auth.AccountIdentitySubject,
) (bool, error) {
	return true, nil
}

func (a postSeriesCollaborationAuthorizer) AuthorizeInTx(
	ctx context.Context,
	_ *gorm.DB,
	resourceID string,
	permission intrav1.CollaborationPermission,
	subject auth.AccountIdentitySubject,
) (bool, error) {
	return a.Authorize(ctx, resourceID, permission, subject)
}

func TestInternalPostSeriesServicePersistsExactTargetRoom(t *testing.T) {
	documents, db, seriesID, _, _ := newSeriesAIDocumentFixture(t)
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	title, summary := "시리즈", "기존 요약"
	require.NoError(t, db.Table("series_translation").Create(&model.SeriesTranslation{
		EntityID: seriesID, Locale: "ko", Title: &title, Summary: &summary,
		ContentText: &summary, CreatedAt: now, UpdatedAt: now,
	}).Error)
	audit := &postSeriesCollaborationAudit{}
	documents.auditWriter = audit
	subject := auth.AccountIdentitySubject{ID: auth.IdentityID(uuid.NewString())}
	registry := collaboration.NewRegistry(collaboration.Registration{
		ResourceType: intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_POST_SERIES,
		Authorizer:   postSeriesCollaborationAuthorizer{},
	})
	service := NewInternalPostSeriesService(documents, collaboration.NewCheckpointFence(
		registry, postSeriesCollaborationContributorResolver{subject: subject},
	))

	loaded, err := service.LoadDocument(t.Context(), connect.NewRequest(
		&intrav1.LoadPostSeriesDocumentRequest{SeriesId: seriesID, Locale: "ko"},
	))
	require.NoError(t, err)
	require.True(t, loaded.Msg.LocaleExists)
	require.NotNil(t, loaded.Msg.TargetRevision)
	nextSummary := "새 요약"
	saved, err := service.SaveDocument(t.Context(), connect.NewRequest(
		&intrav1.SavePostSeriesDocumentRequest{
			SeriesId: seriesID, Locale: "ko",
			Requested:                &intrav1.PostSeriesLocaleFields{Title: &title, Summary: &nextSummary},
			ContributorMemberIds:     []string{uuid.NewString()},
			ExpectedDocumentRevision: loaded.Msg.DocumentRevision,
			ExpectedTargetRevision:   loaded.Msg.TargetRevision,
		},
	))
	require.NoError(t, err)
	require.Equal(t, loaded.Msg.DocumentRevision, saved.Msg.DocumentRevision)
	require.NotEqual(t, loaded.Msg.TargetRevision, saved.Msg.TargetRevision)
	require.Len(t, audit.records, 1)
	require.Equal(t, "ko", audit.records[0].Locale)
	require.Equal(t, []string{"locale_content"}, audit.records[0].ChangedFields)
}
