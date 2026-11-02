package legal_test

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/legal/legaltest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/telemetry"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func touchPolicy(t *testing.T, kind legal.PolicyKind, id string) policyv1.RelationshipMutation {
	t.Helper()
	build := policyv1.TermsHistory.TouchPolicy
	if kind == legal.Privacy {
		build = policyv1.PrivacyHistory.TouchPolicy
	}
	mutation, err := build(id)
	require.NoError(t, err)
	return mutation
}

func deletePolicy(t *testing.T, kind legal.PolicyKind, id string) policyv1.RelationshipMutation {
	t.Helper()
	build := policyv1.TermsHistory.DeletePolicy
	if kind == legal.Privacy {
		build = policyv1.PrivacyHistory.DeletePolicy
	}
	mutation, err := build(id)
	require.NoError(t, err)
	return mutation
}

func TestNewServiceNamesEveryMissingCollaborator(t *testing.T) {
	_, err := legal.NewService(legal.Deps{})
	require.EqualError(t, err, "legal dependencies are required: Audit, Dispatch, Fence, OGAssets, SpiceDB, Units")
	_, err = legal.NewInternalService(legal.InternalDeps{})
	require.EqualError(t, err, "internal legal dependencies are required: Audit, Fence, SpiceDB, Units")
}

func TestCreatePolicyVersionNumbersTitlesAndGrantsTheDraft(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			ctx := newPrincipal().context(t)
			h.store.PutRun(legaltest.Run{Kind: kind, PolicyID: uuid.NewString(), Version: 4, EventKey: string(kind) + "_update"})

			created, err := h.service.CreatePolicyVersion(ctx, kind, legal.CreatePolicyVersionCommand{
				Document: policyDocument("en", "Body"), AcceptLanguage: "en",
			})
			require.NoError(t, err)
			defaultTitle := map[legal.PolicyKind]string{legal.Terms: "Terms of Service", legal.Privacy: "Privacy Policy"}[kind]
			require.Equal(t, defaultTitle, created.Title)
			require.EqualValues(t, 5, created.Version, "a version sealed by a deleted version's notice is never reused")
			require.Equal(t, status(kind, "draft"), created.Status)
			require.Equal(t, "en", created.SourceLocale)
			require.Equal(t, "Body", created.Document.GetLocaleOverlays()[0].GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
			require.Equal(t, h.revision(t, kind, created.ID), created.Revision)
			require.NotEmpty(t, h.stored(t, kind, created.ID).Projection.HTML, "the derived content is built")
			require.Equal(t, [][]policyv1.RelationshipMutation{{touchPolicy(t, kind, created.ID)}}, h.checker.Applied())
			require.Equal(t, []string{string(sharedtelemetry.AuditLegalPolicyCreated)}, h.actions())

			titled, err := h.service.CreatePolicyVersion(ctx, kind, legal.CreatePolicyVersionCommand{Title: ptr("Explicit")})
			require.NoError(t, err)
			require.Equal(t, "Explicit", titled.Title)
			require.Equal(t, created.Version+1, titled.Version)
		})
	}
}

func TestCreatePolicyVersionPicksTheSourceLocale(t *testing.T) {
	member := newPrincipal()
	for name, test := range map[string]struct {
		preferred      string
		preferredFails bool
		acceptLanguage string
		want           string
	}{
		"the Member's preferred locale":                    {preferred: "ko", acceptLanguage: "ja", want: "ko"},
		"Accept-Language without a preference":             {acceptLanguage: "ja-JP,ja;q=0.9", want: "ja"},
		"Accept-Language when the preference read fails":   {preferredFails: true, acceptLanguage: "ja", want: "ja"},
		"the site default without a preference or request": {want: "en"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			if test.preferred != "" {
				h.store.PutPreferredLocale(member.memberID, test.preferred)
			}
			if test.preferredFails {
				h.store.Fail = map[string]error{"PreferredLocale": errStore}
			}
			created, err := h.service.CreatePolicyVersion(member.context(t), legal.Terms, legal.CreatePolicyVersionCommand{AcceptLanguage: test.acceptLanguage})
			require.NoError(t, err)
			require.Equal(t, test.want, created.SourceLocale)
		})
	}
}

func TestCreatePolicyVersionRollsBackADocumentInAnotherLocale(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.CreatePolicyVersion(newPrincipal().context(t), legal.Privacy, legal.CreatePolicyVersionCommand{
		Document: policyDocument("ko", "Korean body"), AcceptLanguage: "en",
	})
	requireAnswer(t, err, connect.CodeInvalidArgument, "document.source_locale: must match the server-selected source locale")
	require.Equal(t, mutation.StepRollback, h.units.Steps()[len(h.units.Steps())-1])
	require.Empty(t, h.checker.Applied())
	require.Empty(t, h.audit.Records())
	page, err := h.service.ListPolicyVersions(newPrincipal().context(t), legal.Privacy, legal.ListPolicyVersionsQuery{})
	require.NoError(t, err)
	require.Zero(t, page.Total)
}

func TestCreatePolicyVersionRequiresAdministrators(t *testing.T) {
	h := newHarness(t)
	h.checker.Deny(policyv1.TermsHistory.Create())
	_, err := h.service.CreatePolicyVersion(newPrincipal().context(t), legal.Terms, legal.CreatePolicyVersionCommand{})
	requireAnswer(t, err, connect.CodePermissionDenied, "admin access required")
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
}

func TestGetPolicyVersionMasksWhatTheCallerMayNotView(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			caller := newPrincipal()
			ctx := caller.context(t)
			draft, revision := h.putVersion(t, kind, "draft", nil)
			archived, _ := h.putVersion(t, kind, "archived", nil)

			read, err := h.service.GetPolicyVersion(ctx, kind, legal.GetPolicyVersionQuery{ID: draft.ID})
			require.NoError(t, err)
			require.Equal(t, revision, read.Revision)
			require.Equal(t, draft.Title, read.Title)

			missing := uuid.NewString()
			_, err = h.service.GetPolicyVersion(ctx, kind, legal.GetPolicyVersionQuery{ID: missing})
			requireAnswer(t, err, connect.CodeNotFound, notFoundResource(kind)+" not found: "+missing)

			viewArchived := policyv1.TermsHistory.ViewArchived
			if kind == legal.Privacy {
				viewArchived = policyv1.PrivacyHistory.ViewArchived
			}
			h.checker.Deny(viewArchived(archived.ID))
			_, err = h.service.GetPolicyVersion(ctx, kind, legal.GetPolicyVersionQuery{ID: archived.ID})
			requireAnswer(t, err, connect.CodeNotFound, string(kind)+" not found: "+archived.ID)

			h.fence.Deactivate(caller.identityID)
			_, err = h.service.GetPolicyVersion(ctx, kind, legal.GetPolicyVersionQuery{ID: draft.ID})
			requireAnswer(t, err, connect.CodeNotFound, "legal policy not found")
		})
	}
}

func TestListPolicyVersionsPagesNewestFirst(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	first, _ := h.putVersion(t, legal.Terms, "archived", nil)
	second, _ := h.putVersion(t, legal.Terms, "active", nil)
	third, _ := h.putVersion(t, legal.Terms, "draft", nil)
	h.putVersion(t, legal.Privacy, "draft", nil)

	page, err := h.service.ListPolicyVersions(ctx, legal.Terms, legal.ListPolicyVersionsQuery{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, []string{third.ID, second.ID}, []string{page.Versions[0].ID, page.Versions[1].ID})
	require.Equal(t, legal.PolicyPage{Versions: page.Versions, Total: 3, Limit: 2, HasMore: true}, page)
	archived := status(legal.Terms, "archived")
	page, err = h.service.ListPolicyVersions(ctx, legal.Terms, legal.ListPolicyVersionsQuery{Status: &archived})
	require.NoError(t, err)
	require.Len(t, page.Versions, 1)
	require.Equal(t, first.ID, page.Versions[0].ID)
	require.Equal(t, int32(20), page.Limit)

	h.checker.Deny(policyv1.TermsHistory.List())
	_, err = h.service.ListPolicyVersions(ctx, legal.Terms, legal.ListPolicyVersionsQuery{})
	requireAnswer(t, err, connect.CodePermissionDenied, "admin access required")
}

func TestSchedulePolicySealsItsUpdateNoticeAndDispatchesAfterCommit(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			ctx := newPrincipal().context(t)
			draft, revision := h.putVersion(t, kind, "draft", func(v *legal.Version) { v.Title = "September policy" })
			effectiveFrom := time.Now().Add(10 * 24 * time.Hour).UTC().Truncate(time.Second).Add(123456789)

			scheduled, err := h.service.SchedulePolicy(ctx, kind, legal.SchedulePolicyCommand{
				ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &effectiveFrom,
			})
			require.NoError(t, err)
			stored := effectiveFrom.Truncate(time.Microsecond)
			require.Equal(t, status(kind, "scheduled"), scheduled.Status)
			require.True(t, stored.Equal(*scheduled.EffectiveFrom), "sub-microsecond digits PostgreSQL cannot keep never reach the answer")
			require.Equal(t, revision, scheduled.DocumentRevision)
			require.True(t, stored.Equal(*h.stored(t, kind, draft.ID).EffectiveFrom))

			runs := h.store.Runs()
			require.Len(t, runs, 1)
			require.Equal(t, string(kind)+"_update", runs[0].EventKey)
			require.Equal(t, "September policy", runs[0].Data["policy_title"])
			require.Equal(t, stored.Format("2006-01-02"), runs[0].Data["effective_date"])
			require.Regexp(t, `^https://example\.test/s/[^/]+$`, runs[0].Data["preview_url"])
			require.WithinDuration(t, stored.Add(-7*24*time.Hour), runs[0].ScheduledAt, time.Second, "the notice goes out a week ahead")
			require.Empty(t, h.store.Previews(), "the preview link is created when the notice is dispatched")
			require.Equal(t, []string{runs[0].ID}, h.dispatch.dispatched)
			require.Equal(t, []legaltest.OGRequest{{Kind: kind, ID: draft.ID, AllLocales: true, Reason: string(kind) + "_scheduled"}}, h.store.OGRequests())
			require.Equal(t, []legal.PolicyKind{kind}, h.store.ActivationLocks())

			records := h.audit.Records()
			require.Len(t, records, 1)
			require.Equal(t, sharedtelemetry.AuditLegalPolicyUpdated, records[0].Action)
			require.Equal(t, sharedtelemetry.AuditStateDraft, records[0].PreviousState)
			require.Equal(t, sharedtelemetry.AuditStateScheduled, records[0].NewState)

			_, err = h.service.SchedulePolicy(ctx, kind, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &effectiveFrom})
			requireAnswer(t, err, connect.CodeFailedPrecondition, map[legal.PolicyKind]string{
				legal.Terms: "can only schedule draft terms", legal.Privacy: "can only schedule draft privacy policies",
			}[kind])
		})
	}
}

func TestSchedulePolicyRejectsWhatItCannotSchedule(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	draft, revision := h.putVersion(t, legal.Privacy, "draft", nil)
	pending, _ := h.putVersion(t, legal.Privacy, "draft", nil)
	future := time.Now().Add(48 * time.Hour)
	past := time.Now().Add(-time.Hour)

	_, err := h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision})
	requireAnswer(t, err, connect.CodeInvalidArgument, "effective_from is required")
	_, err = h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &past})
	requireAnswer(t, err, connect.CodeInvalidArgument, "effective_from: must be in the future")
	_, err = h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: uuid.NewString(), EffectiveFrom: &future})
	requireAnswer(t, err, connect.CodeFailedPrecondition, "privacy content revision changed; reload before saving")
	missing := uuid.NewString()
	_, err = h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: missing, ExpectedRevision: revision, EffectiveFrom: &future})
	requireAnswer(t, err, connect.CodeNotFound, "privacy policy not found: "+missing)

	h.store.Fail = map[string]error{"SealNoticeRun": errors.New("legal notice email template is unavailable")}
	_, err = h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
	require.Error(t, err)
	require.Equal(t, status(legal.Privacy, "draft"), h.stored(t, legal.Privacy, draft.ID).Status, "a notice that cannot be sealed rolls the schedule back")
	h.store.Fail = nil

	_, err = h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
	require.NoError(t, err)
	_, err = h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: pending.ID, ExpectedRevision: h.revision(t, legal.Privacy, pending.ID), EffectiveFrom: &future})
	requireAnswer(t, err, connect.CodeFailedPrecondition, "another privacy policy version is already scheduled")
	require.Equal(t, status(legal.Privacy, "draft"), h.stored(t, legal.Privacy, pending.ID).Status)
}

func TestSchedulePolicyKeepsTheScheduleWhenDispatchFails(t *testing.T) {
	h := newHarness(t)
	h.dispatch.err = errors.New("dispatcher unavailable")
	draft, revision := h.putVersion(t, legal.Terms, "draft", nil)
	future := time.Now().Add(48 * time.Hour)
	_, err := h.service.SchedulePolicy(newPrincipal().context(t), legal.Terms, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
	require.NoError(t, err, "the scheduler retries a dispatch that failed after the commit")
	require.Equal(t, status(legal.Terms, "scheduled"), h.stored(t, legal.Terms, draft.ID).Status)
}

func TestCancelPolicyScheduleReturnsToDraftAndRevokesItsPreview(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			ctx := newPrincipal().context(t)
			draft, revision := h.putVersion(t, kind, "draft", nil)
			future := time.Now().Add(48 * time.Hour)
			_, err := h.service.SchedulePolicy(ctx, kind, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
			require.NoError(t, err)
			run := h.store.Runs()[0]
			require.NoError(t, legal.PrepareAutomaticNoticePreviewShareLink(ctx, h.store, run.NoticeRun, time.Now()))
			require.Len(t, h.store.Previews(), 1)

			cancelled, err := h.service.CancelPolicySchedule(ctx, kind, legal.CancelPolicyScheduleCommand{ID: draft.ID})
			require.NoError(t, err)
			require.Equal(t, status(kind, "draft"), cancelled.Status)
			require.Nil(t, cancelled.EffectiveFrom)
			require.Equal(t, revision, cancelled.DocumentRevision)
			require.Equal(t, legaltest.RunCancelled, h.store.Runs()[0].Status)
			require.Empty(t, h.store.Previews())
			require.Equal(t, []legal.PolicyKind{kind}, h.store.Released(), "cancelling the only scheduled version releases the route")
			records := h.audit.Records()
			require.Equal(t, sharedtelemetry.AuditStateScheduled, records[1].PreviousState)
			require.Equal(t, sharedtelemetry.AuditStateDraft, records[1].NewState)
			require.NotNil(t, records[1].EffectiveAt)

			_, err = h.service.CancelPolicySchedule(ctx, kind, legal.CancelPolicyScheduleCommand{ID: draft.ID})
			requireAnswer(t, err, connect.CodeFailedPrecondition, map[legal.PolicyKind]string{
				legal.Terms: "can only cancel scheduled terms", legal.Privacy: "can only cancel scheduled privacy policies",
			}[kind])
		})
	}
}

func TestCancelPolicyScheduleMovesTheRouteBackToTheActiveVersion(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	past := time.Now().Add(-time.Hour)
	active, _ := h.putVersion(t, legal.Terms, "active", func(v *legal.Version) { v.EffectiveFrom = &past })
	future := time.Now().Add(time.Hour)
	scheduled, _ := h.putVersion(t, legal.Terms, "scheduled", func(v *legal.Version) { v.EffectiveFrom = &future })
	other, _ := h.putVersion(t, legal.Privacy, "scheduled", func(v *legal.Version) { v.EffectiveFrom = &future })

	_, err := h.service.CancelPolicySchedule(ctx, legal.Terms, legal.CancelPolicyScheduleCommand{ID: scheduled.ID})
	require.NoError(t, err)
	require.Empty(t, h.store.OGRequests(), "the route keeps showing the active version")

	h.store.Fail = map[string]error{}
	_, err = h.service.CancelPolicySchedule(ctx, legal.Privacy, legal.CancelPolicyScheduleCommand{ID: other.ID})
	require.NoError(t, err)
	require.Equal(t, []legal.PolicyKind{legal.Privacy}, h.store.Released())
	_ = active
}

func TestCancelPolicyScheduleRejectsAStartedNotice(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	draft, revision := h.putVersion(t, legal.Privacy, "draft", nil)
	future := time.Now().Add(48 * time.Hour)
	_, err := h.service.SchedulePolicy(ctx, legal.Privacy, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
	require.NoError(t, err)
	h.store.SetRunStatus(h.store.Runs()[0].ID, legaltest.RunSending)

	_, err = h.service.CancelPolicySchedule(ctx, legal.Privacy, legal.CancelPolicyScheduleCommand{ID: draft.ID})
	requireAnswer(t, err, connect.CodeFailedPrecondition, "legal notice delivery has already started")
	require.Equal(t, status(legal.Privacy, "scheduled"), h.stored(t, legal.Privacy, draft.ID).Status)
	require.Equal(t, legaltest.RunSending, h.store.Runs()[0].Status)
}

func TestActivatePolicyNowArchivesThePreviousVersionAndSealsItsEffectiveNotice(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			ctx := newPrincipal().context(t)
			past := time.Now().Add(-time.Hour)
			previous, _ := h.putVersion(t, kind, "active", func(v *legal.Version) { v.EffectiveFrom = &past })
			draft, revision := h.putVersion(t, kind, "draft", nil)
			future := time.Now().Add(48 * time.Hour)
			_, err := h.service.SchedulePolicy(ctx, kind, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
			require.NoError(t, err)

			activated, err := h.service.ActivatePolicyNow(ctx, kind, legal.ActivatePolicyNowCommand{ID: draft.ID, ExpectedRevision: revision})
			require.NoError(t, err)
			require.Equal(t, status(kind, "active"), activated.Status)
			require.WithinDuration(t, time.Now(), *activated.EffectiveFrom, time.Minute, "an immediate activation takes effect now")
			require.Equal(t, status(kind, "archived"), h.stored(t, kind, previous.ID).Status)
			require.NotNil(t, h.stored(t, kind, previous.ID).EffectiveUntil)

			runs := h.store.Runs()
			require.Len(t, runs, 2)
			effectiveKey := map[legal.PolicyKind]string{legal.Terms: "terms_url", legal.Privacy: "privacy_url"}[kind]
			require.Equal(t, string(kind)+"_effective", runs[1].EventKey)
			require.Equal(t, map[string]string{effectiveKey: "https://example.test/" + string(kind)}, runs[1].Data)
			require.Equal(t, []string{string(kind) + "/" + draft.ID}, h.dispatch.cancelled, "the obsolete update notice is cancelled after the commit")
			require.Equal(t, []string{runs[0].ID, runs[1].ID}, h.dispatch.dispatched)
			require.Equal(t, string(kind)+"_activated", h.store.OGRequests()[len(h.store.OGRequests())-1].Reason)

			records := h.audit.Records()[1:]
			require.Len(t, records, 2, "the archived predecessor is recorded before the activated version")
			require.Equal(t, previous.ID, records[0].TargetID)
			require.Equal(t, sharedtelemetry.AuditStateArchived, records[0].NewState)
			require.Equal(t, draft.ID, records[1].TargetID)
			require.Equal(t, sharedtelemetry.AuditStateScheduled, records[1].PreviousState)
			require.Equal(t, sharedtelemetry.AuditStateActive, records[1].NewState)

			_, err = h.service.ActivatePolicyNow(ctx, kind, legal.ActivatePolicyNowCommand{ID: previous.ID, ExpectedRevision: h.revision(t, kind, previous.ID)})
			requireAnswer(t, err, connect.CodeFailedPrecondition, "can only activate draft or scheduled "+notFoundResource(kind))
		})
	}
}

func TestActivatePolicyNowRollsBackWithoutAnEffectiveNoticeTemplate(t *testing.T) {
	h := newHarness(t)
	draft, revision := h.putVersion(t, legal.Privacy, "draft", nil)
	h.store.Fail = map[string]error{"SealNoticeRun": errors.New("active legal notice email template is unavailable")}

	_, err := h.service.ActivatePolicyNow(newPrincipal().context(t), legal.Privacy, legal.ActivatePolicyNowCommand{ID: draft.ID, ExpectedRevision: revision})
	require.Error(t, err)
	require.Equal(t, status(legal.Privacy, "draft"), h.stored(t, legal.Privacy, draft.ID).Status)
	require.Empty(t, h.store.Runs())
	require.Empty(t, h.dispatch.dispatched)
	require.Empty(t, h.dispatch.cancelled)
	require.Empty(t, h.audit.Records())
}

func TestDeletePolicyKeepsNoticeHistoryAndRevokesTheVersion(t *testing.T) {
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t)
			ctx := newPrincipal().context(t)
			past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
			fallback, _ := h.putVersion(t, kind, "scheduled", func(v *legal.Version) { v.EffectiveFrom = &future })
			current, revision := h.putVersion(t, kind, "active", func(v *legal.Version) { v.EffectiveFrom = &past })
			sent := h.store.PutRun(legaltest.Run{Kind: kind, PolicyID: current.ID, EventKey: string(kind) + "_effective", Status: legaltest.RunSent})
			h.store.PutTranslation(kind, current.ID, legal.Translation{Locale: "ko", Title: ptr("Korean")})

			require.NoError(t, h.service.DeletePolicy(ctx, kind, legal.DeletePolicyCommand{ID: current.ID, ExpectedRevision: revision}))
			_, exists := h.store.Record(kind, current.ID)
			require.False(t, exists)
			_, exists = h.store.StoredTranslation(kind, current.ID, "ko")
			require.False(t, exists)
			_, exists = h.store.Document(uuid.MustParse(*current.ContentDocumentID))
			require.False(t, exists)
			runs := h.store.Runs()
			require.Equal(t, sent.ID, runs[0].ID, "delivery history outlives the deleted version")
			require.True(t, runs[0].StoppedAfter)
			require.Equal(t, legaltest.RunSent, runs[0].Status)
			shareType, _ := kind.ShareLinkEntityType()
			require.Equal(t, []string{shareType + "/" + current.ID}, h.store.LinkDeletes())
			require.Equal(t, []legaltest.OGRequest{{Kind: kind, ID: fallback.ID, AllLocales: true, Reason: string(kind) + "_policy_deleted"}}, h.store.OGRequests())
			require.Equal(t, [][]policyv1.RelationshipMutation{{deletePolicy(t, kind, current.ID)}}, h.checker.Applied())
			require.Equal(t, []string{string(sharedtelemetry.AuditLegalPolicyDeleted)}, h.actions())
			require.Equal(t, []string{string(kind) + "/" + current.ID}, h.ogAssets.released)

			_, err := h.service.GetPolicyVersion(ctx, kind, legal.GetPolicyVersionQuery{ID: current.ID})
			requireAnswer(t, err, connect.CodeNotFound, "")
			err = h.service.DeletePolicy(ctx, kind, legal.DeletePolicyCommand{ID: current.ID, ExpectedRevision: revision})
			requireAnswer(t, err, connect.CodeNotFound, notFoundResource(kind)+" not found: "+current.ID)
		})
	}
}

func TestDeletePolicyReleasesTheRouteOfTheOnlyCurrentVersion(t *testing.T) {
	h := newHarness(t)
	h.ogAssets.err = errors.New("asset release failed")
	future := time.Now().Add(time.Hour)
	scheduled, revision := h.putVersion(t, legal.Terms, "scheduled", func(v *legal.Version) { v.EffectiveFrom = &future })
	draft, draftRevision := h.putVersion(t, legal.Terms, "draft", nil)
	ctx := newPrincipal().context(t)

	require.NoError(t, h.service.DeletePolicy(ctx, legal.Terms, legal.DeletePolicyCommand{ID: draft.ID, ExpectedRevision: draftRevision}))
	require.Empty(t, h.store.Released(), "a version the route does not show leaves the route alone")
	require.NoError(t, h.service.DeletePolicy(ctx, legal.Terms, legal.DeletePolicyCommand{ID: scheduled.ID, ExpectedRevision: revision}),
		"a failed image release after the commit is only logged")
	require.Equal(t, []legal.PolicyKind{legal.Terms}, h.store.Released())
}

func TestDeletePolicyRejectsAStaleRevision(t *testing.T) {
	h := newHarness(t)
	draft, _ := h.putVersion(t, legal.Privacy, "draft", nil)
	err := h.service.DeletePolicy(newPrincipal().context(t), legal.Privacy, legal.DeletePolicyCommand{ID: draft.ID, ExpectedRevision: uuid.NewString()})
	requireAnswer(t, err, connect.CodeFailedPrecondition, "privacy content revision changed; reload before saving")
	h.stored(t, legal.Privacy, draft.ID)
	require.Empty(t, h.checker.Applied())
}

func TestRegeneratePolicyDerivedContentRequiresTheCurrentDigest(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	past := time.Now().Add(-time.Hour)
	active, _ := h.putVersion(t, legal.Terms, "active", func(v *legal.Version) { v.EffectiveFrom = &past })
	read, err := h.service.GetPolicyVersion(ctx, legal.Terms, legal.GetPolicyVersionQuery{ID: active.ID})
	require.NoError(t, err)

	_, err = h.service.RegeneratePolicyDerivedContent(ctx, legal.Terms, legal.RegeneratePolicyDerivedContentCommand{ID: active.ID, ExpectedSnapshotDigest: "stale"})
	requireAnswer(t, err, connect.CodeFailedPrecondition, "legal derived-content regeneration targets a stale Block document")
	require.Empty(t, h.stored(t, legal.Terms, active.ID).Projection.HTML)

	digest, err := h.service.RegeneratePolicyDerivedContent(ctx, legal.Terms, legal.RegeneratePolicyDerivedContentCommand{ID: active.ID, ExpectedSnapshotDigest: read.SnapshotDigest})
	require.NoError(t, err)
	require.Equal(t, read.SnapshotDigest, digest)
	projection := h.stored(t, legal.Terms, active.ID).Projection
	require.Equal(t, read.SnapshotDigest, projection.Digest)
	require.Contains(t, projection.HTML, "Body of")
	require.Equal(t, "terms_derived_content_regenerated", h.store.OGRequests()[0].Reason)
	require.Empty(t, h.audit.Records(), "derived content changes no policy fact")
}

func TestActivateDuePolicyActivatesOnlyADueScheduledVersion(t *testing.T) {
	h := newHarness(t)
	audit := h.audit
	activator, err := legal.NewScheduledActivator(h.units, audit)
	require.NoError(t, err)
	now := time.Now().UTC()
	later := now.Add(time.Hour)
	notDue, _ := h.putVersion(t, legal.Privacy, "scheduled", func(v *legal.Version) { v.EffectiveFrom = &later })
	data := map[string]string{"privacy_url": "https://example.test/privacy"}

	activated, run, err := activator.ActivateDuePolicy(t.Context(), legal.Privacy, notDue.ID, email.EventPrivacyEffective.String(), data, now)
	require.NoError(t, err)
	require.False(t, activated)
	require.Nil(t, run)
	activated, _, err = activator.ActivateDuePolicy(t.Context(), legal.Privacy, uuid.NewString(), email.EventPrivacyEffective.String(), data, now)
	require.NoError(t, err)
	require.False(t, activated, "a version that is gone is not due")

	activated, run, err = activator.ActivateDuePolicy(t.Context(), legal.Privacy, notDue.ID, email.EventPrivacyEffective.String(), data, later)
	require.NoError(t, err)
	require.True(t, activated)
	require.Equal(t, h.store.Runs()[0].ID, run.ID)
	record := h.stored(t, legal.Privacy, notDue.ID)
	require.Equal(t, status(legal.Privacy, "active"), record.Status)
	require.True(t, later.Equal(*record.EffectiveFrom), "a due activation keeps its scheduled effective time")
	records := audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.ActorKindSystem, records[0].Kind)
	require.Equal(t, []string{"status"}, records[0].ChangedFields)
}

func TestPrepareAutomaticNoticePreviewShareLinkCreatesTheLinkOnce(t *testing.T) {
	h := newHarness(t)
	ctx := newPrincipal().context(t)
	draft, revision := h.putVersion(t, legal.Terms, "draft", nil)
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
	_, err := h.service.SchedulePolicy(ctx, legal.Terms, legal.SchedulePolicyCommand{ID: draft.ID, ExpectedRevision: revision, EffectiveFrom: &future})
	require.NoError(t, err)
	run := h.store.Runs()[0].NoticeRun

	require.NoError(t, legal.PrepareAutomaticNoticePreviewShareLink(ctx, h.store, run, time.Now()))
	require.NoError(t, legal.PrepareAutomaticNoticePreviewShareLink(ctx, h.store, run, time.Now()), "a resumed dispatch finds the link it created")
	previews := h.store.Previews()
	require.Len(t, previews, 1)
	for token, preview := range previews {
		require.Equal(t, legal.AutomaticPreview{
			Token: token, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(), EntityID: draft.ID,
			CreatedAt: preview.CreatedAt, ExpiresAt: future,
		}, preview)
	}
	require.Error(t, legal.PrepareAutomaticNoticePreviewShareLink(ctx, h.store, run, future.Add(time.Second)), "the preview closes when the version takes effect")

	effective := h.store.PutRun(legaltest.Run{Kind: legal.Terms, PolicyID: draft.ID, EventKey: email.EventTermsEffective.String(), Data: map[string]string{"terms_url": "https://example.test/terms"}})
	require.NoError(t, legal.PrepareAutomaticNoticePreviewShareLink(ctx, h.store, effective.NoticeRun, time.Now()), "an effective notice has no preview")
	require.Len(t, h.store.Previews(), 1)
}

// The handler leaves logging to the request record and keeps the store
// cause in the returned error.
func TestTermsRPCKeepsTheStoreCauseForTheRequestRecord(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	h := newHarness(t)
	h.store.Fail = map[string]error{"Version": errors.New(`store failed: column "title" <policy> {constraint detail} reader@example.test password=private-password`)}

	_, err := legal.NewTermsConnectHandler(h.service).GetTermsVersion(newPrincipal().context(t), connect.NewRequest(&managev1.GetTermsVersionRequest{Id: "terms-1"}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	require.Empty(t, output.String())
	cause := failurecause.Message(err)
	require.Contains(t, cause, `store failed: column "title" <policy> {constraint detail}`)
	require.NotContains(t, cause, "reader@example.test")
	require.NotContains(t, cause, "private-password")
}

func ptr(value string) *string { return &value }

func TestScheduledPolicyActivationRollsBackBeforeSealingWhenAuditFails(t *testing.T) {
	h := newHarness(t)
	now := time.Now().UTC()
	dueAt := now.Add(-time.Minute)
	active, _ := h.putVersion(t, legal.Terms, "active", nil)
	scheduled, _ := h.putVersion(t, legal.Terms, "scheduled", func(v *legal.Version) { v.EffectiveFrom = &dueAt })
	failed := errors.New("system audit failed")
	h.audit.Err = failed
	activator, err := legal.NewScheduledActivator(h.units, h.audit)
	require.NoError(t, err)
	activated, run, err := activator.ActivateDuePolicy(t.Context(), legal.Terms, scheduled.ID, email.EventTermsEffective.String(), nil, now)
	require.ErrorIs(t, err, failed)
	require.False(t, activated)
	require.Nil(t, run)
	require.Equal(t, status(legal.Terms, "active"), h.stored(t, legal.Terms, active.ID).Status)
	require.Equal(t, status(legal.Terms, "scheduled"), h.stored(t, legal.Terms, scheduled.ID).Status)
	require.Empty(t, h.store.Runs())
}
