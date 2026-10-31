//go:build integration

package programevent

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An AI source edit leaves the same Member locale-content record as a
// collaboration source save; only target edits were audited before.
func TestProgramEventAIDocumentSourceEditIsAuditedIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Program event AI audit admin")
	ctx := programEventAuditedMemberContext(t, identityID, memberID)
	spiceDB := integrationSpiceDB(t)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	store := newProgramEventIntegrationContentBlockStore(t, spiceDB)
	writer := apitelemetry.NewDurableWriter(db)
	suffix := testutil.IntegrationUUID()
	eventType, err := NewAuditedProgramEventTypeService(db, writer, spiceDB).CreateProgramEventType(ctx, connect.NewRequest(&managev1.CreateProgramEventTypeRequest{Slug: "ai-audit-type-" + suffix, Locale: "en", Name: "AI audit"}))
	require.NoError(t, err)
	service := NewAuditedProgramEventService(
		db, newProgramEventRuntime("https://cdn.example.test"), newProgramEventIntegrationFileService(db, spiceDB), writer, spiceDB,
		newProgramEventCreditMemberSummaries(db, "https://cdn.example.test"),
		WithProgramEventContentBlockStore(store), WithProgramEventAsyncPublisher(&programEventAIDocumentTestPublisher{}),
	)
	created, err := service.CreateProgramEvent(ctx, connect.NewRequest(&managev1.CreateProgramEventRequest{
		Title: "AI audited event", Slug: "ai-audit-event-" + suffix, SourceLocale: "en", TypeId: eventType.Msg.Id,
		StartsAt: timestamppb.New(time.Now().UTC().Add(time.Hour)), Timezone: "UTC", LocationMode: managev1.ProgramEventLocationMode_PROGRAM_EVENT_LOCATION_MODE_ONLINE,
	}))
	require.NoError(t, err)
	eventID := created.Msg.Id
	before := programEventAuditCount(t, db, eventID)

	applied, err := service.ExecuteAIDocumentCommand(ctx, eventID, "en", AIDocumentExecutionApply, func(state AIDocumentState) (AIDocumentCommand, error) {
		batch, err := contentblock.BatchFromRichTextProto(state.ContentDocumentID, programEventParagraphMutationBatch(state.LocalizedDocument, state.DocumentRevision, testutil.IntegrationUUID(), "en", "AI source body", []string{memberID}, true))
		if err != nil {
			return AIDocumentCommand{}, err
		}
		command := programEventMetadataCommandForState(state)
		command.Batch = &batch
		return command, nil
	})
	require.NoError(t, err)
	require.True(t, applied.Changed)

	require.Equal(t, before+1, programEventAuditCount(t, db, eventID))
	rows := programEventAuditRows(t, db)
	requireProgramEventAudit(t, rows, string(sharedtelemetry.AuditProgramEventUpdated), "program_event", eventID, `{"changed_fields":["locale_content"],"locale":"en","item_operation":"updated"}`)
	for _, row := range rows {
		if row.TargetID == eventID {
			require.Equal(t, memberID, row.ActorMemberID)
		}
	}
}
