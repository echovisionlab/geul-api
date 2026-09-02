//go:build integration

package form_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFormPersonalDataAccessPersistsExactOperationScopesIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	admin := auth.GetUser(adminCtx)
	require.NotNil(t, admin)
	now := time.Now().UTC().Truncate(time.Second)
	writer := apitelemetry.NewDurableWriter(db)
	service := newAuditedFormServiceForIntegration(
		t, db, admin.IdentityID.String(), writer, writer, spiceDB,
	)

	formID := seedFormSourceLocaleBaseRowAt(t, db, now)
	seedFormSourceLocaleTranslationRow(
		t, db, formID, "en", "Access form",
		[]byte(`{"steps":[{"id":"step-1","fields":[{"id":"field-1","key":"email","label":"Email","type":"text"}]}]}`),
		nil, now,
	)
	submissionID := uuid.NewString()
	require.NoError(t, db.Create(&model.FormSubmission{
		ID: submissionID, FormID: formID,
		Data: []byte(`{"field-1":"private-value@example.test"}`), CreatedAt: now,
	}).Error)

	_, err := service.ListFormSubmissions(
		formPersonalAccessAdminContext(t, admin),
		connect.NewRequest(&managev1.ListFormSubmissionsRequest{FormId: formID}),
	)
	require.NoError(t, err)
	_, err = service.GetFormSubmissionStats(
		formPersonalAccessAdminContext(t, admin),
		connect.NewRequest(&managev1.GetFormSubmissionStatsRequest{FormId: formID}),
	)
	require.NoError(t, err)
	_, err = service.GetFormSubmissionWithSchema(
		formPersonalAccessAdminContext(t, admin),
		connect.NewRequest(&managev1.GetFormSubmissionRequest{Id: submissionID}),
	)
	require.NoError(t, err)

	type storedAccess struct {
		Action, ActorMemberID, RequestID, SourceIP       string
		SubjectType, SubjectID, AccessKind, DataCategory string
	}
	var records []storedAccess
	require.NoError(t, db.Raw(`
		SELECT action, actor_member_id::text AS actor_member_id, request_id::text AS request_id,
		       host(source_ip) AS source_ip,
		       attributes->>'subject_type' AS subject_type,
		       attributes->>'subject_id' AS subject_id,
		       attributes->>'access_kind' AS access_kind,
		       attributes->>'data_category' AS data_category
		FROM security_access
		WHERE action = 'personal_data.accessed' AND actor_member_id = ?::uuid
		ORDER BY occurred_at, access_id
	`, admin.MemberID.String()).Scan(&records).Error)
	require.Len(t, records, 3)

	wantScopes := map[string]int{
		"form:" + formID + ":form_submissions":                 2,
		"form_submission:" + submissionID + ":form_submission": 1,
	}
	requestIDs := make(map[string]struct{}, len(records))
	for _, record := range records {
		require.Equal(t, string(sharedtelemetry.SecurityPersonalDataAccessed), record.Action)
		require.Equal(t, admin.MemberID.String(), record.ActorMemberID)
		require.Equal(t, "198.51.100.23", record.SourceIP)
		require.Equal(t, "read", record.AccessKind)
		require.NotContains(t, strings.Join([]string{record.SubjectType, record.SubjectID, record.DataCategory}, ":"), "private-value@example.test")
		wantScopes[record.SubjectType+":"+record.SubjectID+":"+record.DataCategory]--
		requestIDs[record.RequestID] = struct{}{}
	}
	for scope, remaining := range wantScopes {
		require.Zero(t, remaining, scope)
	}
	require.Len(t, requestIDs, 3)
}

func formPersonalAccessAdminContext(t *testing.T, admin *auth.UserInfo) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("198.51.100.23")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), admin)
}
