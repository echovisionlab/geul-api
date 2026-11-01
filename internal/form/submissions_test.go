package form_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Every submission read records exactly the personal data it opened, for
// the reader.
func TestSubmissionReadsRecordTheirPersonalDataAccess(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	submission := h.store.PutSubmission(form.Submission{
		FormID: stored.ID, Data: []byte(`{"email":"johndoe@example.com"}`), CountryCode: new("KR"), CreatedAt: time.Now().UTC(),
	})
	admin := newPrincipal()
	ctx := admin.accessContext(t)

	listed, err := h.forms.ListFormSubmissions(ctx, connect.NewRequest(&managev1.ListFormSubmissionsRequest{FormId: stored.ID, CountryCode: new("kr")}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Submissions, 1)
	require.Equal(t, &commonv1.PaginationResponse{Total: 1, Limit: 50}, listed.Msg.Pagination)
	stats, err := h.forms.GetFormSubmissionStats(ctx, connect.NewRequest(&managev1.GetFormSubmissionStatsRequest{FormId: stored.ID}))
	require.NoError(t, err)
	require.Equal(t, int32(1), stats.Msg.Stats.TotalSubmissions)
	require.Equal(t, "Email", stats.Msg.Stats.FieldStats[0].FieldLabel)
	withSchema, err := h.forms.GetFormSubmissionWithSchema(ctx, connect.NewRequest(&managev1.GetFormSubmissionRequest{Id: submission.ID}))
	require.NoError(t, err)
	require.Equal(t, "Contact", withSchema.Msg.FormTitle)
	require.JSONEq(t, sourceSchema, string(withSchema.Msg.FormSchema))
	require.Equal(t, submission.ID, withSchema.Msg.Submission.Id)

	records := h.audit.AccessRecords()
	require.Len(t, records, 3)
	scopes := make([]string, len(records))
	for i, record := range records {
		require.Equal(t, sharedtelemetry.SecurityPersonalDataAccessed, record.Action)
		require.Equal(t, admin.memberID, record.MemberID)
		require.Equal(t, "198.51.100.23", record.SourceIP)
		require.Equal(t, sharedtelemetry.PersonalDataAccessRead, record.AccessKind)
		scopes[i] = record.SubjectType + ":" + record.SubjectID + ":" + record.DataCategory
	}
	require.Equal(t, []string{
		"form:" + stored.ID + ":form_submissions", "form:" + stored.ID + ":form_submissions",
		"form_submission:" + submission.ID + ":form_submission",
	}, scopes)
}

func TestSubmissionReadsFailClosedWithoutTheirAccessRecord(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	h.audit.Err = errors.New("security writer unavailable")
	_, err := h.forms.ListFormSubmissions(newPrincipal().accessContext(t), connect.NewRequest(&managev1.ListFormSubmissionsRequest{FormId: stored.ID}))
	requireCode(t, err, connect.CodeUnavailable, "personal data access record is temporarily unavailable")
}

func TestSubmissionReadsHideFormsTheCallerMayNotManage(t *testing.T) {
	h := newHarnessWith(t, &authztest.Checker{})
	stored := h.seedForm(t, nil)
	submission := h.store.PutSubmission(form.Submission{FormID: stored.ID, Data: []byte(`{}`)})
	ctx := newPrincipal().context(t)
	missing := uuid.NewString()

	_, err := h.forms.ListFormSubmissions(ctx, connect.NewRequest(&managev1.ListFormSubmissionsRequest{FormId: stored.ID}))
	requireCode(t, err, connect.CodeNotFound, "form not found: "+stored.ID)
	_, err = h.forms.ListFormSubmissions(ctx, connect.NewRequest(&managev1.ListFormSubmissionsRequest{FormId: missing}))
	requireCode(t, err, connect.CodeNotFound, "form not found: "+missing)
	_, err = h.forms.GetFormSubmissionStats(ctx, connect.NewRequest(&managev1.GetFormSubmissionStatsRequest{FormId: stored.ID}))
	requireCode(t, err, connect.CodeNotFound, "form not found: "+stored.ID)
	_, err = h.forms.GetFormSubmissionStats(ctx, connect.NewRequest(&managev1.GetFormSubmissionStatsRequest{FormId: " "}))
	requireCode(t, err, connect.CodeInvalidArgument, "form_id is required")
	_, err = h.forms.GetFormSubmissionWithSchema(ctx, connect.NewRequest(&managev1.GetFormSubmissionRequest{Id: submission.ID}))
	requireCode(t, err, connect.CodeNotFound, "submission not found")
	_, err = h.forms.GetFormSubmissionWithSchema(ctx, connect.NewRequest(&managev1.GetFormSubmissionRequest{Id: missing}))
	requireCode(t, err, connect.CodeNotFound, "submission not found")
	require.Empty(t, h.audit.AccessRecords())

	h.checker.Allow(policyv1.Form.Manage(stored.ID))
	_, err = h.forms.ListFormSubmissions(ctx, connect.NewRequest(&managev1.ListFormSubmissionsRequest{FormId: stored.ID, CountryCode: new("KOR")}))
	requireCode(t, err, connect.CodeInvalidArgument, "country_code: must be a two-letter country code")
}

func TestDeleteFormSubmissionAuditsOnceAndRollsBackWithItsAudit(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	submission := h.store.PutSubmission(form.Submission{FormID: stored.ID, Data: []byte(`{"private_answer":"must not audit"}`)})
	admin := newPrincipal()

	_, err := h.forms.DeleteFormSubmission(admin.context(t), connect.NewRequest(&managev1.DeleteFormSubmissionRequest{Id: submission.ID}))
	require.NoError(t, err)
	_, err = h.forms.DeleteFormSubmission(admin.context(t), connect.NewRequest(&managev1.DeleteFormSubmissionRequest{Id: submission.ID}))
	requireCode(t, err, connect.CodeNotFound, "submission not found")
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditFormSubmissionDeleted, records[0].Action)
	require.Equal(t, submission.ID, records[0].TargetID)
	require.Equal(t, admin.memberID, records[0].MemberID)

	kept := h.store.PutSubmission(form.Submission{FormID: stored.ID, Data: []byte(`{}`)})
	h.audit.Err = errors.New("audit unavailable")
	_, err = h.forms.DeleteFormSubmission(admin.context(t), connect.NewRequest(&managev1.DeleteFormSubmissionRequest{Id: kept.ID}))
	require.ErrorIs(t, err, h.audit.Err)
	require.Equal(t, mutation.StepRollback, lastStep(h.units))
	require.Len(t, h.store.StoredSubmissions(stored.ID), 1)
}
