//go:build integration

package form_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	formCharacterizationSourceSchema = `{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email","required":true}]}]}`
	formCharacterizationTargetSchema = `{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email","required":true}]}]}`
)

// These cases pin every SaveDocument branch before it is decomposed: the
// response revisions, both locale rows, the root timestamp, the Content
// Document revision, the Domain Audit record and the OG generation request.
func TestFormSaveDocumentCharacterizationIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	spiceDB := integrationSpiceDB(t)
	identityID := testutil.IntegrationUUID()
	contributorID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Form save contributor "+identityID[:8])
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	outsiderIdentityID := testutil.IntegrationUUID()
	outsiderID := testutil.SeedPostIntegrationIdentity(t, db, outsiderIdentityID, "Form save outsider "+outsiderIdentityID[:8])
	service := newAuditedInternalFormServiceForIntegration(db, nil, apitelemetry.NewDurableWriter(db), spiceDB)
	seededAt := time.Unix(1_700_100_000, 0).UTC()

	setup := func(t *testing.T) (string, formSaveState) {
		t.Helper()
		formID := seedFormSourceLocaleBaseRowAt(t, db, seededAt)
		seedFormSourceLocaleTranslationRow(t, db, formID, "en", "Contact", []byte(formCharacterizationSourceSchema), ptrString("Contact\nEmail"), seededAt)
		seedFormSourceLocaleTranslationRow(t, db, formID, "ko", "문의 양식", []byte(formCharacterizationTargetSchema), ptrString("문의\n이메일"), seededAt)
		return formID, loadFormSaveState(t, db, formID)
	}
	load := func(t *testing.T, formID, locale string) *intrav1.LoadFormDocumentResponse {
		t.Helper()
		loaded, err := service.LoadDocument(t.Context(), connect.NewRequest(&intrav1.LoadFormDocumentRequest{FormId: formID, Locale: locale}))
		require.NoError(t, err)
		return loaded.Msg
	}
	save := func(
		formID, locale, revision string,
		targetRevision *string,
		title *string,
		schema string,
		loaded *intrav1.LoadFormDocumentResponse,
	) (*connect.Response[intrav1.SaveFormDocumentResponse], error) {
		return service.SaveDocument(t.Context(), connect.NewRequest(formSaveRequest(
			formID, locale, revision, targetRevision, title, schema, loaded, contributorID,
		)))
	}

	t.Run("source title and schema advance the document", func(t *testing.T) {
		formID, before := setup(t)
		loaded := load(t, formID, "en")
		require.Equal(t, before.documentRevision, loaded.DocumentRevision)
		schema := `{"id":"source-schema","steps":[{"id":"step-1","title":"Reach us","fields":[{"id":"field-email","key":"email","label":"Your email","type":"email","required":true}]}]}`
		response, err := save(formID, "en", loaded.DocumentRevision, nil, ptrString("Updated title"), schema, loaded)
		require.NoError(t, err)
		require.True(t, response.Msg.Success)
		require.Equal(t, "en", response.Msg.Locale)
		require.Nil(t, response.Msg.TargetRevision)
		require.NotEqual(t, before.documentRevision, response.Msg.DocumentRevision)

		after := loadFormSaveState(t, db, formID)
		require.Equal(t, response.Msg.DocumentRevision, after.documentRevision)
		source := after.locales["en"]
		require.Equal(t, ptrString("Updated title"), source.Title)
		require.JSONEq(t, schema, source.ContentJSON)
		require.Equal(t, ptrString("Reach us\nYour email"), source.ContentText)
		require.True(t, source.UpdatedAt.After(seededAt))
		require.Equal(t, source.UpdatedAt, after.rootUpdatedAt)
		require.Equal(t, before.locales["ko"], after.locales["ko"])
		requireFormSaveAudits(t, db, formID, contributorID, "en")
		// The target keeps its own title, so only the source locale is regenerated.
		require.Equal(t, []string{"en"}, formOGTargetLocales(t, db, formID))
	})

	t.Run("source schema only skips OG", func(t *testing.T) {
		formID, before := setup(t)
		loaded := load(t, formID, "en")
		schema := `{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Work email","type":"email","required":true}]}]}`
		response, err := save(formID, "en", loaded.DocumentRevision, nil, ptrString("Contact"), schema, loaded)
		require.NoError(t, err)
		require.NotEqual(t, before.documentRevision, response.Msg.DocumentRevision)
		after := loadFormSaveState(t, db, formID)
		require.Equal(t, response.Msg.DocumentRevision, after.documentRevision)
		require.Equal(t, ptrString("Contact"), after.locales["en"].Title)
		require.JSONEq(t, schema, after.locales["en"].ContentJSON)
		requireFormSaveAudits(t, db, formID, contributorID, "en")
		require.Empty(t, formOGTargetLocales(t, db, formID))
	})

	t.Run("source no-op keeps every row", func(t *testing.T) {
		formID, before := setup(t)
		loaded := load(t, formID, "en")
		response, err := save(formID, "en", loaded.DocumentRevision, nil, ptrString("Contact"), formCharacterizationSourceSchema, loaded)
		require.NoError(t, err)
		require.Equal(t, &intrav1.SaveFormDocumentResponse{Success: true, Locale: "en", DocumentRevision: before.documentRevision}, response.Msg)
		require.Equal(t, before, loadFormSaveState(t, db, formID))
		requireFormSaveAudits(t, db, formID, contributorID)
		require.Empty(t, formOGTargetLocales(t, db, formID))
	})

	t.Run("target title advances only the target revision", func(t *testing.T) {
		formID, before := setup(t)
		loaded := load(t, formID, "ko")
		require.NotNil(t, loaded.TargetRevision)
		response, err := save(formID, "ko", loaded.DocumentRevision, loaded.TargetRevision, ptrString("새 제목"), formCharacterizationTargetSchema, loaded)
		require.NoError(t, err)
		require.Equal(t, "ko", response.Msg.Locale)
		require.Equal(t, before.documentRevision, response.Msg.DocumentRevision)
		require.NotNil(t, response.Msg.TargetRevision)
		require.NotEqual(t, *loaded.TargetRevision, *response.Msg.TargetRevision)
		require.Equal(t, response.Msg.TargetRevision, load(t, formID, "ko").TargetRevision)

		after := loadFormSaveState(t, db, formID)
		require.Equal(t, before.documentRevision, after.documentRevision)
		require.Equal(t, ptrString("새 제목"), after.locales["ko"].Title)
		require.True(t, after.locales["ko"].UpdatedAt.After(seededAt))
		require.Equal(t, after.locales["ko"].UpdatedAt, after.rootUpdatedAt)
		require.Equal(t, before.locales["en"], after.locales["en"])
		requireFormSaveAudits(t, db, formID, contributorID, "ko")
		require.Equal(t, []string{"ko"}, formOGTargetLocales(t, db, formID))
	})

	t.Run("target no-op returns the current target revision", func(t *testing.T) {
		formID, before := setup(t)
		loaded := load(t, formID, "ko")
		response, err := save(formID, "ko", loaded.DocumentRevision, loaded.TargetRevision, ptrString("문의 양식"), formCharacterizationTargetSchema, loaded)
		require.NoError(t, err)
		require.Equal(t, &intrav1.SaveFormDocumentResponse{
			Success: true, Locale: "ko", DocumentRevision: before.documentRevision, TargetRevision: loaded.TargetRevision,
		}, response.Msg)
		require.Equal(t, before, loadFormSaveState(t, db, formID))
		requireFormSaveAudits(t, db, formID, contributorID)
	})

	t.Run("rejections", func(t *testing.T) {
		formID, before := setup(t)
		source := load(t, formID, "en")
		target := load(t, formID, "ko")
		missingFormID := testutil.IntegrationUUID()
		staleRevision := testutil.IntegrationUUID()
		staleTarget := testutil.IntegrationUUID()
		movedSchema := `{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email","required":false}]}]}`
		cases := []struct {
			name     string
			request  *intrav1.SaveFormDocumentRequest
			code     connect.Code
			message  string
			conflict *intrav1.CollaborationConflictReason
		}{
			{
				name:    "blank locale",
				request: &intrav1.SaveFormDocumentRequest{FormId: formID, Locale: " "},
				code:    connect.CodeInvalidArgument, message: "locale is required",
			},
			{
				name:    "non-canonical revision",
				request: &intrav1.SaveFormDocumentRequest{FormId: formID, Locale: "en", ExpectedDocumentRevision: " " + source.DocumentRevision},
				code:    connect.CodeInvalidArgument, message: "expected_document_revision: must be a canonical UUID",
			},
			{
				name:    "missing schema",
				request: &intrav1.SaveFormDocumentRequest{FormId: formID, Locale: "en", ExpectedDocumentRevision: source.DocumentRevision},
				code:    connect.CodeInvalidArgument, message: "meta: canonical Form schema is required",
			},
			{
				name: "present values mismatch",
				request: formSaveRequest(formID, "en", source.DocumentRevision, nil, nil, formCharacterizationSourceSchema,
					source, contributorID),
				code: connect.CodeInvalidArgument, message: "present_locale_values: present_locale_values must exactly match canonical Form locale values",
			},
			{
				name: "missing form",
				request: formSaveRequest(missingFormID, "en", source.DocumentRevision, nil, ptrString("Contact"), formCharacterizationSourceSchema,
					source, contributorID),
				code: connect.CodeNotFound, message: "form not found: " + missingFormID,
			},
			{
				name: "stale document revision",
				request: formSaveRequest(formID, "en", staleRevision, nil, ptrString("Stale"), formCharacterizationSourceSchema,
					source, contributorID),
				code: connect.CodeFailedPrecondition, message: "Form Content Document revision changed",
				conflict: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED.Enum(),
			},
			{
				name: "two contributors",
				request: formSaveRequest(formID, "en", source.DocumentRevision, nil, ptrString("Contact"), formCharacterizationSourceSchema,
					source, contributorID, outsiderID),
				code: connect.CodeInvalidArgument, message: "contributor_member_ids: Form collaboration mutation requires exactly one origin Member",
			},
			{
				name: "non-canonical contributor",
				request: formSaveRequest(formID, "en", source.DocumentRevision, nil, ptrString("Contact"), formCharacterizationSourceSchema,
					source, "not-a-member"),
				code: connect.CodeInvalidArgument, message: "contributor_member_ids: Form collaboration mutation requires one canonical Member UUID",
			},
			{
				name: "contributor without edit permission",
				request: formSaveRequest(formID, "en", source.DocumentRevision, nil, ptrString("Outsider"), formCharacterizationSourceSchema,
					source, outsiderID),
				code: connect.CodePermissionDenied, message: "no permission to COLLABORATION_PERMISSION_EDIT this collaboration resource",
			},
			{
				name: "absent locale row",
				request: formSaveRequest(formID, "ja", source.DocumentRevision, nil, ptrString("Contact"), formCharacterizationSourceSchema,
					source, contributorID),
				code: connect.CodeFailedPrecondition, message: "Form locale document does not exist",
			},
			{
				name: "source with target revision",
				request: formSaveRequest(formID, "en", source.DocumentRevision, &staleTarget, ptrString("Contact"), formCharacterizationSourceSchema,
					source, contributorID),
				code: connect.CodeInvalidArgument, message: "expected_target_revision: source locale has no target revision",
			},
			{
				name: "stale target revision",
				request: formSaveRequest(formID, "ko", target.DocumentRevision, &staleTarget, ptrString("Stale"), formCharacterizationTargetSchema,
					target, contributorID),
				code: connect.CodeFailedPrecondition, message: "translation target revision conflict",
				conflict: intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED.Enum(),
			},
			{
				name: "target changes shared topology",
				request: formSaveRequest(formID, "ko", target.DocumentRevision, target.TargetRevision, ptrString("문의 양식"), movedSchema,
					target, contributorID),
				code: connect.CodeInvalidArgument, message: "meta.schema: form target schema may change locale copy only",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := service.SaveDocument(t.Context(), connect.NewRequest(tc.request))
				require.Error(t, err)
				require.Equal(t, tc.code, connect.CodeOf(err))
				var connectErr *connect.Error
				require.ErrorAs(t, err, &connectErr)
				require.Equal(t, tc.message, connectErr.Message())
				if tc.conflict != nil {
					detail := formCollaborationConflictDetail(t, connectErr)
					require.Equal(t, *tc.conflict, detail.GetReason())
					require.Equal(t, before.documentRevision, detail.GetCurrentDocumentRevision())
				}
			})
		}
		_, err := service.SaveDocument(t.Context(), connect.NewRequest[intrav1.SaveFormDocumentRequest](nil))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		require.Equal(t, before, loadFormSaveState(t, db, formID))
		requireFormSaveAudits(t, db, formID, contributorID)
		require.Empty(t, formOGTargetLocales(t, db, formID))
	})
}

func formSaveRequest(
	formID, locale, revision string,
	targetRevision *string,
	title *string,
	schema string,
	loaded *intrav1.LoadFormDocumentResponse,
	contributors ...string,
) *intrav1.SaveFormDocumentRequest {
	return &intrav1.SaveFormDocumentRequest{
		FormId: formID, Locale: locale, ExpectedDocumentRevision: revision, ExpectedTargetRevision: targetRevision,
		Meta:                 &intrav1.FormMeta{Title: title, Schema: &schema},
		PresentLocaleValues:  loaded.GetPresentLocaleValues(),
		ContributorMemberIds: contributors,
	}
}

type formSaveLocaleRow struct {
	Locale      string    `gorm:"column:locale"`
	Title       *string   `gorm:"column:title"`
	ContentJSON string    `gorm:"column:content_json"`
	ContentText *string   `gorm:"column:content_text"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

type formSaveState struct {
	documentRevision string
	rootUpdatedAt    time.Time
	locales          map[string]formSaveLocaleRow
}

func loadFormSaveState(t *testing.T, db *gorm.DB, formID string) formSaveState {
	t.Helper()
	var root struct {
		Revision  string    `gorm:"column:revision"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	require.NoError(t, db.Raw(`SELECT document.revision::text AS revision, root.updated_at
		FROM form AS root JOIN content_document AS document ON document.id = root.content_document_id
		WHERE root.id = ?`, formID).Scan(&root).Error)
	var rows []formSaveLocaleRow
	require.NoError(t, db.Raw(`SELECT locale, title, content_json::text AS content_json, content_text, updated_at
		FROM form_translation WHERE entity_id = ? ORDER BY locale`, formID).Scan(&rows).Error)
	state := formSaveState{
		documentRevision: root.Revision, rootUpdatedAt: root.UpdatedAt.UTC(),
		locales: make(map[string]formSaveLocaleRow, len(rows)),
	}
	for _, row := range rows {
		row.UpdatedAt = row.UpdatedAt.UTC()
		state.locales[row.Locale] = row
	}
	return state
}

// requireFormSaveAudits asserts one locale-content Audit per listed locale,
// each attributed to the contributing Member.
func requireFormSaveAudits(t *testing.T, db *gorm.DB, formID, contributorID string, locales ...string) {
	t.Helper()
	var rows []formAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, attributes
		FROM domain_audit WHERE target_type = 'form' AND target_id = ? ORDER BY occurred_at, audit_id`, formID).Scan(&rows).Error)
	require.Len(t, rows, len(locales))
	for index, locale := range locales {
		require.Equal(t, string(sharedtelemetry.AuditFormUpdated), rows[index].Action)
		require.Equal(t, contributorID, rows[index].ActorMemberID)
		require.JSONEq(t, `{"changed_fields":["locale_content"],"locale":"`+locale+`","item_operation":"updated"}`, string(rows[index].Attributes))
	}
}

func formOGTargetLocales(t *testing.T, db *gorm.DB, formID string) []string {
	t.Helper()
	var locales []string
	require.NoError(t, db.Raw(`SELECT target.locale FROM og_generation AS generation
		JOIN og_generation_target AS target ON target.id = generation.target_id
		WHERE target.entity_type = 'form' AND target.entity_id = ? ORDER BY target.locale`, formID).Scan(&locales).Error)
	return locales
}

func formCollaborationConflictDetail(t *testing.T, err *connect.Error) *intrav1.CollaborationConflictDetail {
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
