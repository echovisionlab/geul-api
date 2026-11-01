package form_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (h *harness) load(t *testing.T, formID, locale string) *intrav1.LoadFormDocumentResponse {
	t.Helper()
	loaded, err := h.internal.LoadDocument(collabContext(t), connect.NewRequest(&intrav1.LoadFormDocumentRequest{FormId: formID, Locale: locale}))
	require.NoError(t, err)
	return loaded.Msg
}

func saveRequest(
	formID, locale, revision string,
	targetRevision *string,
	title *string,
	schema string,
	loaded *intrav1.LoadFormDocumentResponse,
	contributors ...string,
) *connect.Request[intrav1.SaveFormDocumentRequest] {
	return connect.NewRequest(&intrav1.SaveFormDocumentRequest{
		FormId: formID, Locale: locale, ExpectedDocumentRevision: revision, ExpectedTargetRevision: targetRevision,
		Meta:                 &intrav1.FormMeta{Title: title, Schema: &schema},
		PresentLocaleValues:  loaded.GetPresentLocaleValues(),
		ContributorMemberIds: contributors,
	})
}

// requireLocaleAudits asserts one locale-content record per listed locale,
// each attributed to the contributor although editor-collab saved as itself.
func requireLocaleAudits(t *testing.T, h *harness, contributor string, locales ...string) {
	t.Helper()
	records := h.audit.Records()
	require.Len(t, records, len(locales))
	for i, locale := range locales {
		require.Equal(t, sharedtelemetry.AuditFormUpdated, records[i].Action)
		require.Equal(t, sharedtelemetry.ActorKindMember, records[i].Kind)
		require.Equal(t, contributor, records[i].MemberID)
		require.Equal(t, []string{"locale_content"}, records[i].ChangedFields)
		require.Equal(t, locale, records[i].Locale)
		require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[i].ItemOperation)
	}
}

func TestLoadFormDocumentProjectsTheSourceAndTargetLocales(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)

	source := h.load(t, stored.ID, "en")
	require.Equal(t, "en", source.SourceLocale)
	require.True(t, source.LocaleExists)
	require.Equal(t, h.store.DocumentRevisionOf(stored.ContentDocumentID).String(), source.DocumentRevision)
	require.Equal(t, "Contact", source.GetLocaleMetadata().GetTitle())
	require.Nil(t, source.TargetRevision)
	require.Len(t, source.PresentLocaleValues, 3, "the title, the step title and the field label")

	target := h.load(t, stored.ID, "ko")
	require.Equal(t, "문의 양식", target.GetLocaleMetadata().GetTitle())
	require.Equal(t, "Contact", target.GetSourceMetadata().GetTitle())
	require.NotNil(t, target.TargetRevision)

	absent := h.load(t, stored.ID, "ja")
	require.False(t, absent.LocaleExists)
	require.Nil(t, absent.LocaleMetadata)
	require.Nil(t, absent.PresentLocaleValues)

	_, err := h.internal.LoadDocument(collabContext(t), connect.NewRequest(&intrav1.LoadFormDocumentRequest{FormId: stored.ID, Locale: " "}))
	requireCode(t, err, connect.CodeInvalidArgument, "locale is required")
}

func TestSaveFormDocumentAdvancesTheSourceRevisionForItsContributor(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	contributor := uuid.NewString()
	loaded := h.load(t, stored.ID, "en")
	schema := `{"id":"source-schema","steps":[{"id":"step-1","title":"Reach us","fields":[{"id":"field-email","key":"email","label":"Your email","type":"email","required":true}]}]}`

	response, err := h.internal.SaveDocument(collabContext(t), saveRequest(stored.ID, "en", loaded.DocumentRevision, nil, new("Updated title"), schema, loaded, contributor))

	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.Equal(t, "en", response.Msg.Locale)
	require.Nil(t, response.Msg.TargetRevision)
	require.NotEqual(t, loaded.DocumentRevision, response.Msg.DocumentRevision)
	require.Equal(t, response.Msg.DocumentRevision, h.store.DocumentRevisionOf(stored.ContentDocumentID).String())
	source, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Updated title", *source.Title)
	require.Equal(t, "Reach us\nYour email", *source.ContentText)
	saved, _ := h.store.Stored(stored.ID)
	require.True(t, saved.UpdatedAt.Equal(source.UpdatedAt), "the root moves with the locale")
	require.Equal(t, []string{contributor}, h.store.Contributions())
	requireLocaleAudits(t, h, contributor, "en")
	// The target keeps its own title, so only the source locale is regenerated.
	require.Equal(t, []formtest.OGRequest{{FormID: stored.ID, Locale: "en", AllLocales: true, Reason: "form_document_saved"}}, h.store.OGRequests())
}

func TestSaveFormDocumentOfOnlyASchemaSkipsOG(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	contributor := uuid.NewString()
	loaded := h.load(t, stored.ID, "en")
	schema := `{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Work email","type":"email","required":true}]}]}`

	response, err := h.internal.SaveDocument(collabContext(t), saveRequest(stored.ID, "en", loaded.DocumentRevision, nil, new("Contact"), schema, loaded, contributor))

	require.NoError(t, err)
	require.NotEqual(t, loaded.DocumentRevision, response.Msg.DocumentRevision)
	requireLocaleAudits(t, h, contributor, "en")
	require.Empty(t, h.store.OGRequests())
}

func TestSaveFormDocumentNoOpKeepsEveryRow(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	contributor := uuid.NewString()
	source := h.load(t, stored.ID, "en")
	target := h.load(t, stored.ID, "ko")
	beforeSource, _ := h.store.StoredLocale(stored.ID, "en")

	response, err := h.internal.SaveDocument(collabContext(t), saveRequest(stored.ID, "en", source.DocumentRevision, nil, new("Contact"), sourceSchema, source, contributor))
	require.NoError(t, err)
	require.Equal(t, &intrav1.SaveFormDocumentResponse{Success: true, Locale: "en", DocumentRevision: source.DocumentRevision}, response.Msg)
	targetResponse, err := h.internal.SaveDocument(collabContext(t), saveRequest(stored.ID, "ko", target.DocumentRevision, target.TargetRevision, new("문의 양식"), targetSchema, target, contributor))
	require.NoError(t, err)
	require.Equal(t, &intrav1.SaveFormDocumentResponse{
		Success: true, Locale: "ko", DocumentRevision: source.DocumentRevision, TargetRevision: target.TargetRevision,
	}, targetResponse.Msg)

	afterSource, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, beforeSource, afterSource)
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.OGRequests())
}

func TestSaveFormDocumentAdvancesOnlyTheTargetRevision(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	contributor := uuid.NewString()
	loaded := h.load(t, stored.ID, "ko")

	response, err := h.internal.SaveDocument(collabContext(t), saveRequest(stored.ID, "ko", loaded.DocumentRevision, loaded.TargetRevision, new("새 제목"), targetSchema, loaded, contributor))

	require.NoError(t, err)
	require.Equal(t, "ko", response.Msg.Locale)
	require.Equal(t, loaded.DocumentRevision, response.Msg.DocumentRevision)
	require.NotEqual(t, *loaded.TargetRevision, *response.Msg.TargetRevision)
	require.Equal(t, response.Msg.TargetRevision, h.load(t, stored.ID, "ko").TargetRevision)
	target, _ := h.store.StoredLocale(stored.ID, "ko")
	require.Equal(t, "새 제목", *target.Title)
	requireLocaleAudits(t, h, contributor, "ko")
	require.Equal(t, []formtest.OGRequest{{FormID: stored.ID, Locale: "ko", Reason: "form_document_saved"}}, h.store.OGRequests())
}

func TestSaveFormDocumentRejections(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	contributor, outsider := uuid.NewString(), uuid.NewString()
	h.store.Contributors = map[string]error{outsider: errs.PermissionDenied("a collaboration contributor is no longer active")}
	source := h.load(t, stored.ID, "en")
	target := h.load(t, stored.ID, "ko")
	missing, staleRevision, staleTarget := uuid.NewString(), uuid.NewString(), uuid.NewString()
	movedSchema := `{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email","required":false}]}]}`
	documentConflict := intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED
	targetConflict := intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED
	for _, test := range []struct {
		name     string
		request  *connect.Request[intrav1.SaveFormDocumentRequest]
		code     connect.Code
		message  string
		conflict *intrav1.CollaborationConflictReason
	}{
		{name: "blank locale", request: connect.NewRequest(&intrav1.SaveFormDocumentRequest{FormId: stored.ID, Locale: " "}), code: connect.CodeInvalidArgument, message: "locale is required"},
		{name: "non-canonical revision", request: connect.NewRequest(&intrav1.SaveFormDocumentRequest{FormId: stored.ID, Locale: "en", ExpectedDocumentRevision: " " + source.DocumentRevision}), code: connect.CodeInvalidArgument, message: "expected_document_revision: must be a canonical UUID"},
		{name: "missing schema", request: connect.NewRequest(&intrav1.SaveFormDocumentRequest{FormId: stored.ID, Locale: "en", ExpectedDocumentRevision: source.DocumentRevision}), code: connect.CodeInvalidArgument, message: "meta: canonical Form schema is required"},
		{name: "present values mismatch", request: saveRequest(stored.ID, "en", source.DocumentRevision, nil, nil, sourceSchema, source, contributor), code: connect.CodeInvalidArgument, message: "present_locale_values: present_locale_values must exactly match canonical Form locale values"},
		{name: "missing form", request: saveRequest(missing, "en", source.DocumentRevision, nil, new("Contact"), sourceSchema, source, contributor), code: connect.CodeNotFound, message: "form not found: " + missing},
		{name: "stale document revision", request: saveRequest(stored.ID, "en", staleRevision, nil, new("Stale"), sourceSchema, source, contributor), code: connect.CodeFailedPrecondition, message: "Form Content Document revision changed", conflict: &documentConflict},
		{name: "two contributors", request: saveRequest(stored.ID, "en", source.DocumentRevision, nil, new("Contact"), sourceSchema, source, contributor, outsider), code: connect.CodeInvalidArgument, message: "contributor_member_ids: Form collaboration mutation requires exactly one origin Member"},
		{name: "non-canonical contributor", request: saveRequest(stored.ID, "en", source.DocumentRevision, nil, new("Contact"), sourceSchema, source, "not-a-member"), code: connect.CodeInvalidArgument, message: "contributor_member_ids: Form collaboration mutation requires one canonical Member UUID"},
		{name: "contributor without edit permission", request: saveRequest(stored.ID, "en", source.DocumentRevision, nil, new("Outsider"), sourceSchema, source, outsider), code: connect.CodePermissionDenied, message: "a collaboration contributor is no longer active"},
		{name: "absent locale", request: saveRequest(stored.ID, "ja", source.DocumentRevision, nil, new("Contact"), sourceSchema, source, contributor), code: connect.CodeFailedPrecondition, message: "Form locale document does not exist"},
		{name: "source with target revision", request: saveRequest(stored.ID, "en", source.DocumentRevision, &staleTarget, new("Contact"), sourceSchema, source, contributor), code: connect.CodeInvalidArgument, message: "expected_target_revision: source locale has no target revision"},
		{name: "stale target revision", request: saveRequest(stored.ID, "ko", target.DocumentRevision, &staleTarget, new("Stale"), targetSchema, target, contributor), code: connect.CodeFailedPrecondition, message: "translation target revision conflict", conflict: &targetConflict},
		{name: "target changes shared topology", request: saveRequest(stored.ID, "ko", target.DocumentRevision, target.TargetRevision, new("문의 양식"), movedSchema, target, contributor), code: connect.CodeInvalidArgument, message: "meta.schema: form target schema may change locale copy only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := h.internal.SaveDocument(collabContext(t), test.request)
			require.Error(t, err)
			var connectErr *connect.Error
			require.ErrorAs(t, err, &connectErr)
			require.Equal(t, test.code, connectErr.Code())
			require.Equal(t, test.message, connectErr.Message())
			if test.conflict != nil {
				detail := conflictDetail(t, connectErr)
				require.Equal(t, *test.conflict, detail.GetReason())
				require.Equal(t, source.DocumentRevision, detail.GetCurrentDocumentRevision())
			}
		})
	}
	_, err := h.internal.SaveDocument(collabContext(t), connect.NewRequest[intrav1.SaveFormDocumentRequest](nil))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Equal(t, source.DocumentRevision, h.store.DocumentRevisionOf(stored.ContentDocumentID).String())
	require.Empty(t, h.audit.Records())
	require.Empty(t, h.store.OGRequests())
}

// A save whose audit fails leaves the document and its revision as they
// were.
func TestSaveFormDocumentRollsBackWhenItsAuditFails(t *testing.T) {
	h := newHarness(t)
	stored := h.seedForm(t, nil)
	loaded := h.load(t, stored.ID, "en")
	h.audit.Err = errors.New("audit unavailable")

	_, err := h.internal.SaveDocument(collabContext(t), saveRequest(stored.ID, "en", loaded.DocumentRevision, nil, new("Lost"), sourceSchema, loaded, uuid.NewString()))

	require.ErrorIs(t, err, h.audit.Err)
	source, _ := h.store.StoredLocale(stored.ID, "en")
	require.Equal(t, "Contact", *source.Title)
	require.Equal(t, loaded.DocumentRevision, h.store.DocumentRevisionOf(stored.ContentDocumentID).String())
}

func conflictDetail(t *testing.T, err *connect.Error) *intrav1.CollaborationConflictDetail {
	t.Helper()
	for _, detail := range err.Details() {
		value, valueErr := detail.Value()
		require.NoError(t, valueErr)
		if conflict, ok := value.(*intrav1.CollaborationConflictDetail); ok {
			return conflict
		}
	}
	t.Fatalf("collaboration conflict detail is missing from %v", err)
	return nil
}
