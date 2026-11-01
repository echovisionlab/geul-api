package programevent_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/programevent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// room is one collaboration room fixture: an event whose document a
// contributor's session opens.
type room struct {
	h           *harness
	event       programevent.Event
	contributor principal
	sessionID   string
}

func newRoom(t *testing.T, options ...harnessOption) room {
	t.Helper()
	h := newHarness(t, options...)
	contributor := newPrincipal()
	h.store.PutMember(contributor.memberID)
	sessionID := uuid.NewString()
	h.store.Sessions = map[string]*auth.UserInfo{sessionID: contributor.user()}
	return room{h: h, event: h.seedEvent(t), contributor: contributor, sessionID: sessionID}
}

func paragraphBatch(revision uuid.UUID, contributor, locale string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		ExpectedRevision:        revision.String(), ContributorMemberIds: []string{contributor},
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{
				Upsert: &contentv1.UpsertRichTextBlockLocale{Block: &contentv1.RichTextBlockLocale{
					BlockId: uuid.NewString(),
					Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
						Props:   &contentv1.ParagraphLocaleProps{},
						Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "text"}}}},
					}},
				}},
			}}},
		}},
	}
}

func TestLoadBlockDocumentAuthorizesTheSessionPrincipal(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	document, err := r.h.service.LoadBlockDocument(systemContext(t), programevent.LoadBlockDocumentQuery{
		EventID: r.event.ID, Locale: "en", SessionID: &r.sessionID,
	})
	require.NoError(t, err)
	require.Equal(t, "en", document.Locale)
	require.True(t, document.LocaleExists)
	require.Nil(t, document.TargetRevision, "the source locale has no target revision")
	require.Equal(t, &programevent.BlockLocaleMetadata{Locale: "en", Title: text("Event"), Summary: text("Source summary")}, document.SourceMetadata)
	require.Equal(t, r.h.revision(t, r.event).String(), document.DocumentRevision)
	require.Equal(t, []string{r.contributor.identityID}, hydratedIdentities(r.h), "Block media is delivered to the session principal")
	view, err := policyv1.ProgramEvent.View(r.event.ID)
	require.NoError(t, err)
	require.Equal(t, []authztest.Check{authztest.Of(r.contributor.identityID, view, nil)}, r.h.checker.Checks())

	target, err := r.h.service.LoadBlockDocument(systemContext(t), programevent.LoadBlockDocumentQuery{
		EventID: r.event.ID, Locale: "ko", SessionID: &r.sessionID,
	})
	require.NoError(t, err)
	require.False(t, target.LocaleExists)
	require.Nil(t, target.LocaleMetadata)
}

func TestLoadBlockDocumentRejectsWhatTheSessionMayNotOpen(t *testing.T) {
	t.Parallel()
	r := newRoom(t, withChecker(&authztest.Checker{}))
	missing := uuid.NewString()
	// In order: the last case deactivates the principal.
	for _, test := range []struct {
		name    string
		prepare func()
		session *string
		code    connect.Code
		message string
	}{
		{name: "no session", session: nil, code: connect.CodeUnauthenticated, message: "authentication required"},
		{name: "unknown session", session: &missing, code: connect.CodeUnauthenticated, message: "authentication required"},
		{name: "denied", session: &r.sessionID, code: connect.CodePermissionDenied, message: "no permission to view this program event"},
		{name: "inactive", prepare: func() { r.h.store.Fence.Deactivate(r.contributor.identityID) },
			session: &r.sessionID, code: connect.CodePermissionDenied, message: "no permission to edit this program event"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.prepare != nil {
				test.prepare()
			}
			_, err := r.h.service.LoadBlockDocument(systemContext(t), programevent.LoadBlockDocumentQuery{EventID: r.event.ID, Locale: "en", SessionID: test.session})
			requireCode(t, err, test.code, test.message)
		})
	}
}

func TestApplyBlockBatchStoresASourceSaveAttributedToItsContributor(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	before := r.h.revision(t, r.event)

	written, err := r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "en", Batch: paragraphBatch(before, r.contributor.memberID, "en"),
	})

	require.NoError(t, err)
	require.True(t, written.Changed)
	require.True(t, written.SourceChanged)
	require.NotEqual(t, before.String(), written.DocumentRevision)
	stored, _ := r.h.store.StoredEvent(r.event.ID)
	require.True(t, stored.UpdatedAt.After(r.event.UpdatedAt))
	records := r.h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	require.Equal(t, "en", records[0].Locale)
	require.Equal(t, []programevent.ContentUpdate{{
		EventID: r.event.ID, Fields: []string{"content"}, DocumentRevision: written.DocumentRevision,
		Contributors: []string{r.contributor.memberID}, Source: programevent.SourceCollaboration, Locale: "en", DocumentStateChanged: true,
	}}, r.h.store.Updates())

	_, err = r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "en", Batch: paragraphBatch(before, r.contributor.memberID, "en"),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "Program Event content revision changed; reload before saving")
}

func TestApplyBlockBatchWritesATargetLocaleWithoutAdvancingTheDocument(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	before := r.h.revision(t, r.event)
	_, err := r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "ko", Batch: paragraphBatch(before, r.contributor.memberID, "ko"),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "Program Event target locale must be explicitly created before collaboration")
	r.h.store.PutLocale(r.event.ID, programevent.LocaleMetadata{Locale: "ko", Summary: text("기존 요약"), UpdatedAt: at(1)})
	opened, err := r.h.service.LoadBlockDocument(systemContext(t), programevent.LoadBlockDocumentQuery{EventID: r.event.ID, Locale: "ko", SessionID: &r.sessionID})
	require.NoError(t, err)
	require.NotNil(t, opened.TargetRevision)

	written, err := r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "ko", Batch: paragraphBatch(before, r.contributor.memberID, "ko"), ExpectedTargetRevision: opened.TargetRevision,
	})

	require.NoError(t, err)
	require.True(t, written.Changed)
	require.Equal(t, before.String(), written.DocumentRevision)
	require.NotNil(t, written.TargetRevision)
	locale, ok := r.h.store.StoredLocale(r.event.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "기존 요약", *locale.Summary)
	require.True(t, locale.UpdatedAt.After(at(1)), "the target revision clock moves")
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, r.h.audit.Records()[0].ItemOperation)
	update := r.h.store.Updates()[0]
	require.Equal(t, written.TargetRevision, update.TargetRevision)
	require.False(t, update.DocumentStateChanged)

	stale := "stale-target-revision"
	_, err = r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "ko", Batch: paragraphBatch(before, r.contributor.memberID, "ko"), ExpectedTargetRevision: &stale,
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "")

	_, err = r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "en", Batch: paragraphBatch(before, r.contributor.memberID, "en"), ExpectedTargetRevision: &stale,
	})
	requireCode(t, err, connect.CodeInvalidArgument, "expected_target_revision: must be omitted for the source locale")
}

func TestApplyBlockBatchRechecksItsContributorUnderTheRootLock(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	r.h.store.RevokedContributors = map[string][]string{r.event.ID: {r.contributor.memberID}}
	_, err := r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "en", Batch: paragraphBatch(r.h.revision(t, r.event), r.contributor.memberID, "en"),
	})
	requireCode(t, err, connect.CodePermissionDenied, "")
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, r.h.units.Steps())
	require.Empty(t, r.h.store.Updates())

	batch := paragraphBatch(r.h.revision(t, r.event), r.contributor.memberID, "en")
	batch.ContributorMemberIds = append(batch.ContributorMemberIds, uuid.NewString())
	_, err = r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{EventID: r.event.ID, Locale: "en", Batch: batch})
	requireCode(t, err, connect.CodeInvalidArgument, "contributor_member_ids: collaboration mutation requires exactly one origin Member")
	_, err = r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "en", Batch: paragraphBatch(r.h.revision(t, r.event), r.contributor.memberID, "ko"),
	})
	requireCode(t, err, connect.CodeInvalidArgument, "locale: Block mutation must match the authenticated room locale")
}

func TestApplyBlockBatchAnswersADocumentTheEventNoLongerOwns(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	revision := r.h.revision(t, r.event)
	replaced := uuid.NewString()
	r.h.store.Before = map[string]func(*programeventtestStore){"ContentDocumentID": replaceDocumentOnLock(r.event.ID, replaced)}
	_, err := r.h.service.ApplyBlockBatch(systemContext(t), programevent.ApplyBlockBatchCommand{
		EventID: r.event.ID, Locale: "en", Batch: paragraphBatch(revision, r.contributor.memberID, "en"),
	})
	requireCode(t, err, connect.CodeFailedPrecondition, "Program Event content document ownership changed; reload before saving")
}

func TestUpdateLocaleMetadataEditsTheSourceTitleAndSummary(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	before := r.h.revision(t, r.event)

	written, err := r.h.service.UpdateLocaleMetadata(systemContext(t), programevent.UpdateLocaleMetadataCommand{
		EventID: r.event.ID, Locale: "en", Title: text(" Renamed "), Summary: &entityparticipant.StringMutation{Clear: boolPtr(true)},
		ExpectedRevision: before.String(), ContributorMemberIDs: []string{r.contributor.memberID},
	})

	require.NoError(t, err)
	require.True(t, written.Changed)
	require.True(t, written.SourceChanged)
	require.Equal(t, []string{"en"}, written.ChangedLocales)
	require.NotEqual(t, before.String(), written.DocumentRevision)
	stored, _ := r.h.store.StoredEvent(r.event.ID)
	require.Equal(t, "Renamed", stored.Title)
	locale, _ := r.h.store.StoredLocale(r.event.ID, "en")
	require.Nil(t, locale.Summary)
	require.Equal(t, []string{"title", "summary"}, r.h.store.Updates()[0].Fields)

	unchanged, err := r.h.service.UpdateLocaleMetadata(systemContext(t), programevent.UpdateLocaleMetadataCommand{
		EventID: r.event.ID, Locale: "en", Title: text("Renamed"),
		ExpectedRevision: written.DocumentRevision, ContributorMemberIDs: []string{r.contributor.memberID},
	})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Equal(t, written.DocumentRevision, unchanged.DocumentRevision)
	require.Len(t, r.h.audit.Records(), 1, "an unchanged save is unaudited")
}

func TestUpdateLocaleMetadataValidatesTheEdit(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	revision := r.h.revision(t, r.event).String()
	contributors := []string{r.contributor.memberID}
	for message, command := range map[string]programevent.UpdateLocaleMetadataCommand{
		"title: Program Event title is source-owned":      {Locale: "ko", Title: text("Target title")},
		"summary: clear must be true":                     {Locale: "en", Summary: &entityparticipant.StringMutation{Clear: boolPtr(false)}},
		"summary: set or clear is required":               {Locale: "en", Summary: &entityparticipant.StringMutation{}},
		"metadata: title or summary mutation is required": {Locale: "en"},
		"locale: must be an exact canonical locale":       {Locale: "EN", Title: text("Title")},
	} {
		command.EventID, command.ExpectedRevision, command.ContributorMemberIDs = r.event.ID, revision, contributors
		_, err := r.h.service.UpdateLocaleMetadata(systemContext(t), command)
		requireCode(t, err, connect.CodeInvalidArgument, message)
	}
	_, err := r.h.service.UpdateLocaleMetadata(systemContext(t), programevent.UpdateLocaleMetadataCommand{
		EventID: r.event.ID, Locale: "en", Title: text(" "), ExpectedRevision: revision, ContributorMemberIDs: contributors,
	})
	requireCode(t, err, connect.CodeInvalidArgument, "title is required")
	require.Equal(t, mutation.StepRollback, lastStep(t, r.h.units))
}

func TestUpdateLocaleMetadataWritesATargetSummary(t *testing.T) {
	t.Parallel()
	r := newRoom(t)
	r.h.store.PutLocale(r.event.ID, programevent.LocaleMetadata{Locale: "ko", UpdatedAt: at(1)})
	opened, err := r.h.service.LoadBlockDocument(systemContext(t), programevent.LoadBlockDocumentQuery{EventID: r.event.ID, Locale: "ko", SessionID: &r.sessionID})
	require.NoError(t, err)
	written, err := r.h.service.UpdateLocaleMetadata(systemContext(t), programevent.UpdateLocaleMetadataCommand{
		EventID: r.event.ID, Locale: "ko", Summary: &entityparticipant.StringMutation{Set: text("요약")}, ExpectedTargetRevision: opened.TargetRevision,
		ExpectedRevision: r.h.revision(t, r.event).String(), ContributorMemberIDs: []string{r.contributor.memberID},
	})
	require.NoError(t, err)
	require.True(t, written.Changed)
	require.NotNil(t, written.TargetRevision)
	locale, ok := r.h.store.StoredLocale(r.event.ID, "ko")
	require.True(t, ok)
	require.Equal(t, "요약", *locale.Summary)
	require.Equal(t, []string{"summary"}, r.h.store.Updates()[0].Fields)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, r.h.audit.Records()[0].ItemOperation)
}
