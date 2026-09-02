//go:build integration

package form_test

import (
	"connectrpc.com/connect"
	"encoding/json"
	"testing"
	"time"

	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

// Form source collaboration is fenced by the root-owned Content Document
// revision. There is no translation-source state or edit-hash side channel.
func TestFormSourceSaveUsesContentDocumentRevisionCASIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Unix(1_700_009_000, 0).UTC()
	formID := seedFormSourceLocaleBaseRowAt(t, db, now)
	sourceSchema := []byte(`{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]}]}`)
	seedFormSourceLocaleTranslationRow(t, db, formID, "en", "Contact", sourceSchema, ptrString("Contact\nEmail"), now)
	identityID := integrationTestUUID()
	contributorID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Form source mutation contributor")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	policy, err := policyv1.Form.TouchPolicy(formID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), policy)
	require.NoError(t, err)
	service := newAuditedInternalFormServiceForIntegration(db, nil, apitelemetry.NewDurableWriter(db), spiceDB)

	loaded, err := service.LoadDocument(t.Context(), connect.NewRequest(&intrav1.LoadFormDocumentRequest{FormId: formID, Locale: "en"}))
	require.NoError(t, err)
	require.NotEmpty(t, loaded.Msg.DocumentRevision)
	title := "Updated Form"
	schema := string(sourceSchema)
	request := &intrav1.SaveFormDocumentRequest{
		FormId: formID, Locale: "en",
		ExpectedDocumentRevision: loaded.Msg.DocumentRevision,
		Meta:                     &intrav1.FormMeta{Title: &title, Schema: &schema},
		PresentLocaleValues:      loaded.Msg.PresentLocaleValues,
		ContributorMemberIds:     []string{contributorID},
	}
	_, err = service.SaveDocument(t.Context(), connect.NewRequest(request))
	require.NoError(t, err)

	var audit struct {
		ActorMemberID string `gorm:"column:actor_member_id"`
		Attributes    []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Table("domain_audit").Select("actor_member_id::text AS actor_member_id, attributes").Where("action = ? AND target_type = 'form' AND target_id = ?", sharedtelemetry.AuditFormUpdated, formID).Take(&audit).Error)
	var attributes map[string]any
	require.NoError(t, json.Unmarshal(audit.Attributes, &attributes))
	require.Equal(t, contributorID, audit.ActorMemberID)
	require.Equal(t, []any{"locale_content"}, attributes["changed_fields"])
	require.Equal(t, "en", attributes["locale"])
	require.Equal(t, "updated", attributes["item_operation"])

	_, err = service.SaveDocument(t.Context(), connect.NewRequest(request))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
