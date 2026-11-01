package campaign_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type testRuntime struct {
	accessed []string
	err      error
}

func (*testRuntime) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}
func (r *testRuntime) AppendCampaignRecipientsAccess(_ context.Context, id string) error {
	r.accessed = append(r.accessed, id)
	return r.err
}

type harness struct {
	store    *campaigntest.Store
	units    *mutationtest.UnitOfWork[campaign.Store]
	checker  *authztest.Checker
	fence    *identitystatetest.Fence
	audit    *audittest.Recorder
	runtime  *testRuntime
	service  *campaign.Service
	internal *campaign.InternalService
	ctx      context.Context
	now      time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, units := campaigntest.New()
	h := &harness{store: store, units: units, checker: authztest.AllowAll(), fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, runtime: &testRuntime{}, now: time.Now().UTC().Truncate(time.Microsecond)}
	identityID, memberID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{IdentityID: identityID, MemberID: memberID, SessionID: sessionID})
	require.NoError(t, err)
	h.ctx = auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID), SessionID: auth.SessionID(sessionID), Authenticated: true, Onboarded: true})
	h.store.Members[memberID] = true
	h.store.Sessions[sessionID] = auth.GetUser(h.ctx)
	h.service, err = campaign.NewService(campaign.Deps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Runtime: h.runtime, Clock: func() time.Time { return h.now }})
	require.NoError(t, err)
	h.internal, err = campaign.NewInternalService(campaign.InternalDeps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Permissions: h.checker, Clock: func() time.Time { return h.now }})
	require.NoError(t, err)
	return h
}
func code(err error) connect.Code          { return errs.Wrap(err).Code() }
func (h *harness) lastStep() mutation.Step { steps := h.units.Steps(); return steps[len(steps)-1] }
func TestConstructorsRequireThePorts(t *testing.T) {
	_, err := campaign.NewService(campaign.Deps{})
	require.EqualError(t, err, "campaign dependencies are required: Audit, Clock, Fence, Runtime, SpiceDB, Units")
	_, err = campaign.NewInternalService(campaign.InternalDeps{})
	require.EqualError(t, err, "internal campaign dependencies are required: Audit, Clock, Fence, Permissions, SpiceDB, Units")
}
func TestCampaignCRUDOnPorts(t *testing.T) {
	h := newHarness(t)
	created, err := h.service.CreateCampaign(h.ctx, campaign.CreateCampaignCommand{Name: "  News  ", Subject: " Subject ", SourceLocale: "en", Target: campaign.CreateCampaignTarget{All: true}})
	require.NoError(t, err)
	require.Equal(t, "News", created.Name)
	require.Equal(t, "Subject", created.Subject)
	require.NotNil(t, created.Document)
	require.NotEmpty(t, created.DocumentRevision)
	require.Equal(t, "en", created.SourceLocale)
	require.Equal(t, []mutation.Step{mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepRelationships, mutation.StepCommit}, h.units.Steps())
	require.Len(t, h.checker.Applied(), 1)
	require.Contains(t, h.checker.Checks(), authztest.Of(auth.GetUser(h.ctx).IdentityID.String(), mustCan(policyv1.Campaign.Create()), nil))
	records := h.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditCampaignCreated, records[0].Action)
	got, err := h.service.GetCampaign(h.ctx, campaign.GetCampaignQuery{ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, created.ID, got.ID)
	require.Equal(t, created.DocumentRevision, got.DocumentRevision)
	page, err := h.service.ListCampaigns(h.ctx, campaign.ListCampaignsQuery{})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Pagination.Total)
	require.EqualValues(t, 20, page.Pagination.Limit)
	require.Nil(t, page.Campaigns[0].Document)
	require.Equal(t, "en", page.Campaigns[0].SourceLocale)
	renamed, err := h.service.UpdateCampaignName(h.ctx, campaign.UpdateCampaignNameCommand{ID: created.ID, Name: " Renamed "})
	require.NoError(t, err)
	require.True(t, renamed.Changed)
	require.Equal(t, "Renamed", renamed.Name)
	n := len(h.audit.Records())
	renamed, err = h.service.UpdateCampaignName(h.ctx, campaign.UpdateCampaignNameCommand{ID: created.ID, Name: "Renamed"})
	require.NoError(t, err)
	require.False(t, renamed.Changed)
	require.Len(t, h.audit.Records(), n)
	require.NoError(t, h.service.DeleteCampaign(h.ctx, campaign.DeleteCampaignCommand{ID: created.ID}))
	_, exists := h.store.Campaign(created.ID)
	require.False(t, exists)
	require.Len(t, h.checker.Applied(), 2)
	require.Equal(t, mutation.StepCommit, h.lastStep())
}
func mustCan(can policyv1.Can, err error) policyv1.Can {
	if err != nil {
		panic(err)
	}
	return can
}
func TestCreateCampaignValidationAndAssignedAudience(t *testing.T) {
	cases := []struct {
		name     string
		command  campaign.CreateCampaignCommand
		audience *campaign.CampaignAudienceTarget
		want     connect.Code
	}{
		{name: "empty name and subject", command: campaign.CreateCampaignCommand{SourceLocale: "en", Target: campaign.CreateCampaignTarget{All: true}}, want: connect.CodeInvalidArgument},
		{name: "unsupported locale", command: campaign.CreateCampaignCommand{Name: "N", SourceLocale: "xx", Target: campaign.CreateCampaignTarget{All: true}}, want: connect.CodeInvalidArgument},
		{name: "missing target", command: campaign.CreateCampaignCommand{Name: "N", SourceLocale: "en"}, want: connect.CodeInvalidArgument},
		{name: "missing segment", want: connect.CodeNotFound},
		{name: "archived segment", audience: &campaign.CampaignAudienceTarget{Archived: true}, want: connect.CodeFailedPrecondition},
		{name: "assignable segment", audience: &campaign.CampaignAudienceTarget{Valid: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			command := tc.command
			if command.Name == "" && command.SourceLocale == "" {
				segmentID := uuid.NewString()
				command = campaign.CreateCampaignCommand{Name: "N", SourceLocale: "en", Target: campaign.CreateCampaignTarget{SegmentID: &segmentID}}
				if tc.audience != nil {
					h.store.Audiences[segmentID] = *tc.audience
				}
			}
			result, err := h.service.CreateCampaign(h.ctx, command)
			if tc.want != 0 {
				require.Equal(t, tc.want, code(err))
				require.Equal(t, mutation.StepRollback, h.lastStep())
				require.Empty(t, h.audit.Records())
				require.Empty(t, h.checker.Applied())
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, result.ID)
			}
		})
	}
}
func TestMutationFailuresRollBackContentRowsAndRelationships(t *testing.T) {
	for _, failure := range []string{"route", "audit", "relationships"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			want := errors.New(failure + " unavailable")
			switch failure {
			case "route":
				h.store.Fail("EnsureCampaignRoute", want)
			case "audit":
				h.audit.Err = want
			case "relationships":
				h.checker.FailWrites(want)
			}
			_, err := h.service.CreateCampaign(h.ctx, campaign.CreateCampaignCommand{Name: "News", SourceLocale: "en", Target: campaign.CreateCampaignTarget{All: true}})
			require.ErrorIs(t, err, want)
			require.Equal(t, mutation.StepRollback, h.lastStep())
			rows, total, err := h.store.ListCampaigns(h.ctx, campaign.CampaignList{})
			require.NoError(t, err)
			require.Zero(t, total)
			require.Empty(t, rows)
			require.Empty(t, h.checker.Applied())
		})
	}
}
func TestCampaignMutationsAuthorizeAfterRootLockAndRejectInactiveIdentity(t *testing.T) {
	for _, kind := range []string{"rename", "configure", "schedule", "cancel", "send", "delete", "test"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			h.store.Renderable[row.ID] = true
			h.checker.Deny(policyv1.Campaign.Edit(row.ID))
			h.checker.Deny(policyv1.Campaign.Manage(row.ID))
			h.checker.Deny(policyv1.Campaign.Delete(row.ID))
			var err error
			switch kind {
			case "rename":
				_, err = h.service.UpdateCampaignName(h.ctx, campaign.UpdateCampaignNameCommand{ID: row.ID, Name: "N"})
			case "configure":
				layout := ""
				_, err = h.service.UpdateCampaignConfiguration(h.ctx, campaign.UpdateCampaignConfigurationCommand{ID: row.ID, LayoutID: &layout})
			case "schedule":
				_, err = h.service.ScheduleCampaign(h.ctx, campaign.ScheduleCampaignCommand{ID: row.ID})
			case "cancel":
				_, err = h.service.CancelCampaign(h.ctx, campaign.CancelCampaignCommand{ID: row.ID})
			case "send":
				_, err = h.service.SendCampaignNow(h.ctx, campaign.SendCampaignNowCommand{ID: row.ID})
			case "delete":
				err = h.service.DeleteCampaign(h.ctx, campaign.DeleteCampaignCommand{ID: row.ID})
			case "test":
				err = h.service.SendTestCampaign(h.ctx, campaign.SendTestCampaignCommand{ID: row.ID})
			}
			require.Equal(t, connect.CodePermissionDenied, code(err))
			steps := []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}
			if kind == "delete" {
				steps = append([]mutation.Step{mutation.StepAuthorizationLock}, steps...)
			}
			require.Equal(t, steps, h.units.Steps())
			stored, _ := h.store.Campaign(row.ID)
			require.Equal(t, row, stored)
			require.Empty(t, h.audit.Records())
			require.Empty(t, h.store.Queued())
		})
	}
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	h.fence.Deactivate(auth.GetUser(h.ctx).IdentityID.String())
	_, err := h.service.UpdateCampaignName(h.ctx, campaign.UpdateCampaignNameCommand{ID: row.ID, Name: "N"})
	require.Equal(t, connect.CodePermissionDenied, code(err))
	require.Empty(t, h.checker.Checks())
}
func TestConfigurationPatchSemanticsAndAudit(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	layoutID := uuid.NewString()
	segmentID := uuid.NewString()
	h.store.Layouts[layoutID] = campaign.CampaignLayoutReference{ID: layoutID}
	h.store.Audiences[segmentID] = campaign.CampaignAudienceTarget{Valid: true}
	mode, scope := campaign.TargetModeSegment, campaign.RecipientScopeAllMatchingUsers
	result, err := h.service.UpdateCampaignConfiguration(h.ctx, campaign.UpdateCampaignConfigurationCommand{ID: row.ID, TargetMode: &mode, SegmentID: &segmentID, LayoutID: &layoutID, RecipientScope: &scope})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, &segmentID, result.SegmentID)
	require.Equal(t, &layoutID, result.LayoutID)
	require.Equal(t, "ALL_MATCHING_USERS", result.RecipientScope)
	require.Equal(t, []string{"layout", "recipient_scope", "segment", "target_mode"}, h.audit.Records()[0].ChangedFields)
	n := len(h.audit.Records())
	result, err = h.service.UpdateCampaignConfiguration(h.ctx, campaign.UpdateCampaignConfigurationCommand{ID: row.ID, TargetMode: &mode, SegmentID: &segmentID, LayoutID: &layoutID, RecipientScope: &scope})
	require.NoError(t, err)
	require.False(t, result.Changed)
	require.Len(t, h.audit.Records(), n)
	mode = campaign.TargetModeAll
	empty := ""
	result, err = h.service.UpdateCampaignConfiguration(h.ctx, campaign.UpdateCampaignConfigurationCommand{ID: row.ID, TargetMode: &mode, LayoutID: &empty})
	require.NoError(t, err)
	require.Nil(t, result.SegmentID)
	require.Nil(t, result.LayoutID)
	calls := h.store.Calls
	require.Less(t, slices.Index(calls, "LockLayouts"), slices.Index(calls, "LockCampaign"))
}
func TestConfigurationRejectsInvalidAndFrozenPatches(t *testing.T) {
	for _, kind := range []string{"empty", "segment alone", "missing segment", "missing layout", "frozen", "audit rollback"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			command := campaign.UpdateCampaignConfigurationCommand{ID: row.ID}
			value := uuid.NewString()
			mode := campaign.TargetModeSegment
			want := connect.CodeInvalidArgument
			switch kind {
			case "segment alone":
				command.SegmentID = &value
			case "missing segment":
				command.TargetMode = &mode
			case "missing layout":
				command.LayoutID = &value
				want = connect.CodeNotFound
			case "frozen":
				h.store.PutCampaign(campaign.Campaign{ID: row.ID, Status: campaign.CampaignStatusSent})
				empty := ""
				command.LayoutID = &empty
				want = connect.CodeFailedPrecondition
			case "audit rollback":
				command.RecipientScope = new(campaign.RecipientScopeAllMatchingUsers)
				h.audit.Err = errors.New("audit failure")
				want = connect.CodeInternal
			}
			_, err := h.service.UpdateCampaignConfiguration(h.ctx, command)
			require.Equal(t, want, code(err))
			require.Equal(t, mutation.StepRollback, h.lastStep())
			if kind == "audit rollback" {
				stored, _ := h.store.Campaign(row.ID)
				require.Equal(t, row, stored)
			}
		})
	}
}
func TestLifecycleSealsImmutableRunAndCancelsScheduledRun(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	h.store.Renderable[row.ID] = true
	scheduled := h.now.Add(time.Hour)
	lifecycle, err := h.service.ScheduleCampaign(h.ctx, campaign.ScheduleCampaignCommand{ID: row.ID, ScheduledAt: &scheduled, RecipientScope: campaign.RecipientScopeSubscribedUsers})
	require.NoError(t, err)
	require.Equal(t, campaign.CampaignStatusScheduled, lifecycle.Status)
	runs, err := h.store.DueRuns(h.ctx, scheduled, 25)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.True(t, runs[0].DefinitionSealed)
	require.Equal(t, row.ID, *runs[0].CampaignID)
	require.Equal(t, lifecycle.UpdatedAt, *runs[0].SourceCampaignUpdatedAt)
	lifecycle, err = h.service.CancelCampaign(h.ctx, campaign.CancelCampaignCommand{ID: row.ID})
	require.NoError(t, err)
	require.Equal(t, campaign.CampaignStatusDraft, lifecycle.Status)
	require.Nil(t, lifecycle.ScheduledAt)
	run, _ := h.store.Run(runs[0].ID)
	require.Equal(t, "cancelled", run.Status)
}
func TestLifecycleRejectsStaleScopeMissingContentAndInvalidTime(t *testing.T) {
	for _, kind := range []string{"missing time", "past", "missing content", "stale scope", "frozen"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			h.store.Renderable[row.ID] = kind != "missing content"
			scheduled := h.now.Add(time.Hour)
			command := campaign.ScheduleCampaignCommand{ID: row.ID, ScheduledAt: &scheduled, RecipientScope: campaign.RecipientScopeSubscribedUsers}
			want := connect.CodeFailedPrecondition
			switch kind {
			case "missing time":
				command.ScheduledAt = nil
				want = connect.CodeInvalidArgument
			case "past":
				scheduled = h.now.Add(-time.Hour)
				want = connect.CodeInvalidArgument
			case "stale scope":
				command.RecipientScope = campaign.RecipientScopeAllMatchingUsers
			case "frozen":
				h.store.PutCampaign(campaign.Campaign{ID: row.ID, Status: campaign.CampaignStatusSending})
			}
			_, err := h.service.ScheduleCampaign(h.ctx, command)
			require.Equal(t, want, code(err))
			require.Equal(t, mutation.StepRollback, h.lastStep())
			require.Empty(t, h.audit.Records())
		})
	}
}
func TestSendNowDurableHandoffAndRollback(t *testing.T) {
	for _, failure := range []string{"", "no recipients", "queue", "audit"} {
		t.Run(failure, func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			h.store.Renderable[row.ID] = true
			h.store.Candidates = []campaign.DeliveryRecipient{{RecipientEmail: "member@example.test"}}
			want := errors.New("handoff failed")
			switch failure {
			case "no recipients":
				h.store.Candidates = nil
			case "queue":
				h.store.Fail("Enqueue", want)
			case "audit":
				h.audit.Err = want
			}
			result, err := h.service.SendCampaignNow(h.ctx, campaign.SendCampaignNowCommand{ID: row.ID, RecipientScope: campaign.RecipientScopeSubscribedUsers})
			if failure == "" {
				require.NoError(t, err)
				require.Equal(t, campaign.CampaignStatusSending, result.Status)
				require.EqualValues(t, 1, result.RecipientCount)
				require.Len(t, h.store.Queued(), 1)
				require.Equal(t, mutation.StepCommit, h.lastStep())
			} else {
				if failure == "no recipients" {
					require.Equal(t, connect.CodeFailedPrecondition, code(err))
				} else {
					require.ErrorIs(t, err, want)
				}
				stored, _ := h.store.Campaign(row.ID)
				require.Equal(t, row, stored)
				require.Empty(t, h.store.Queued())
				require.Equal(t, mutation.StepRollback, h.lastStep())
			}
		})
	}
}
func TestDeletePreservesHistoryAndRejectsNonDraft(t *testing.T) {
	for _, kind := range []string{"history", "non draft", "constraint", "audit"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			want := connect.CodeFailedPrecondition
			switch kind {
			case "history":
				h.store.PutRun(campaign.DeliveryRun{CampaignID: &row.ID}, campaigntest.Target())
			case "non draft":
				h.store.PutCampaign(campaign.Campaign{ID: row.ID, Status: campaign.CampaignStatusSent})
			case "constraint":
				h.store.Fail("DeleteCampaign", campaign.ErrDurableReference)
			case "audit":
				h.audit.Err = errors.New("audit unavailable")
				want = connect.CodeInternal
			}
			err := h.service.DeleteCampaign(h.ctx, campaign.DeleteCampaignCommand{ID: row.ID})
			require.Equal(t, want, code(err))
			_, ok := h.store.Campaign(row.ID)
			require.True(t, ok)
			_, ok = h.store.Document(uuid.MustParse(*row.ContentDocumentID))
			require.True(t, ok)
			require.Equal(t, mutation.StepRollback, h.lastStep())
			require.Empty(t, h.checker.Applied())
		})
	}
}
func TestStatsRecipientsAccessAndTestSend(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{SentCount: 3})
	stats, err := h.service.GetCampaignStats(h.ctx, campaign.GetCampaignStatsQuery{ID: row.ID})
	require.NoError(t, err)
	require.EqualValues(t, 3, stats.TotalSent)
	run := h.store.PutRun(campaign.DeliveryRun{CampaignID: &row.ID}, campaigntest.Target())
	h.store.PutRecipient(campaign.DeliveryRecipient{RunID: run.ID, Status: "delivered", RecipientEmail: "delivered@example.test"})
	h.store.PutRecipient(campaign.DeliveryRecipient{RunID: run.ID, Status: "suppressed", RecipientEmail: "suppressed@example.test"})
	stats, err = h.service.GetCampaignStats(h.ctx, campaign.GetCampaignStatsQuery{ID: row.ID})
	require.NoError(t, err)
	require.EqualValues(t, 1, stats.TotalSent)
	require.EqualValues(t, 1, stats.TotalSuppressed)
	page, err := h.service.GetCampaignRecipients(h.ctx, campaign.GetCampaignRecipientsQuery{ID: row.ID, Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, page.Recipients, 1)
	require.Equal(t, []string{row.ID}, h.runtime.accessed)
	h.runtime.err = errors.New("access recorder unavailable")
	_, err = h.service.GetCampaignRecipients(h.ctx, campaign.GetCampaignRecipientsQuery{ID: row.ID})
	require.ErrorIs(t, err, h.runtime.err)
	require.NoError(t, h.service.SendTestCampaign(h.ctx, campaign.SendTestCampaignCommand{ID: row.ID, Email: "test@example.test"}))
	require.Len(t, h.store.Queued(), 1)
	require.Empty(t, h.audit.Records())
	steps := h.units.Steps()
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepOutbox, mutation.StepCommit}, steps)
}
func TestCollaborationSourceSubjectAndTargetOwnership(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	document, _ := h.store.Document(uuid.MustParse(*row.ContentDocumentID))
	subject := "New source"
	member := auth.GetUser(h.ctx).MemberID.String()
	change, err := h.internal.UpdateCampaignLocaleMetadata(h.ctx, campaign.UpdateCampaignLocaleMetadataCommand{CampaignID: row.ID, Locale: "en", ExpectedRevision: document.Revision.String(), Subject: &subject, Contributors: []string{member}})
	require.NoError(t, err)
	require.True(t, change.Changed)
	require.True(t, change.SourceChanged)
	stored, _ := h.store.Campaign(row.ID)
	require.Equal(t, subject, stored.Subject)
	require.Less(t, slices.Index(h.store.Calls, "LockMembers"), slices.Index(h.store.Calls, "RequireCurrentContributors"))
	targetSubject := "Target"
	h.store.PutLocale(row.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: &targetSubject, UpdatedAt: h.now}})
	nextDocument, _ := h.store.Document(document.ID)
	_, err = h.internal.UpdateCampaignLocaleMetadata(h.ctx, campaign.UpdateCampaignLocaleMetadataCommand{CampaignID: row.ID, Locale: "ko", ExpectedRevision: nextDocument.Revision.String(), Subject: &subject, Contributors: []string{}})
	require.Equal(t, connect.CodeInvalidArgument, code(err))
}
func TestAIDocumentAuthorizesBeforeCompilerAndValidateRollsBack(t *testing.T) {
	for _, mode := range []emailcontent.AIDocumentExecutionMode{emailcontent.AIDocumentExecutionValidate, emailcontent.AIDocumentExecutionApply} {
		t.Run(string(rune('0'+mode)), func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			compilerCalls := 0
			compiler := func(state emailcontent.AIDocumentState) (emailcontent.AIDocumentMutation, error) {
				compilerCalls++
				return emailcontent.AIDocumentMutation{Reference: row.ID, Locale: "en", ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: "en", ExpectedPresence: true, ContributorMember: uuid.MustParse(auth.GetUser(h.ctx).MemberID.String()), Batch: &contentblock.Batch{DocumentID: state.DocumentID, ExpectedRevision: uuid.MustParse(state.DocumentRevision), ContributorMemberIDs: []uuid.UUID{uuid.MustParse(auth.GetUser(h.ctx).MemberID.String())}}, SetSubject: true, Subject: "AI subject"}, nil
			}
			h.checker.Deny(policyv1.Campaign.Edit(row.ID))
			_, err := h.internal.AIDocuments().ExecuteAIDocumentMutation(h.ctx, row.ID, "en", mode, compiler)
			require.Equal(t, connect.CodePermissionDenied, code(err))
			require.Zero(t, compilerCalls)
			h.checker.Allow(policyv1.Campaign.Edit(row.ID))
			_, err = h.internal.AIDocuments().ExecuteAIDocumentMutation(h.ctx, row.ID, "en", mode, compiler)
			require.NoError(t, err)
			require.Equal(t, 1, compilerCalls)
			stored, _ := h.store.Campaign(row.ID)
			if mode == emailcontent.AIDocumentExecutionValidate {
				require.Equal(t, row.Subject, stored.Subject)
				require.Empty(t, h.audit.Records())
				require.Equal(t, mutation.StepRollback, h.lastStep())
			} else {
				locale, ok := h.store.Locale(row.ID, "en")
				require.True(t, ok)
				require.Equal(t, "AI subject", *locale.Subject)
				require.Len(t, h.audit.Records(), 1)
				require.Equal(t, mutation.StepCommit, h.lastStep())
			}
		})
	}
}

type failedRelationshipUnits struct {
	campaign.Units
	Err error
}

func (u failedRelationshipUnits) RelationshipTransaction(ctx context.Context, writer authzmutation.RelationshipWriter, work func(mutation.Tx[campaign.Store], authzmutation.WriteRelationships) error) error {
	return u.Units.RelationshipTransaction(ctx, writer, func(tx mutation.Tx[campaign.Store], write authzmutation.WriteRelationships) error {
		if err := work(tx, write); err != nil {
			return err
		}
		return u.Err
	})
}
func TestRelationshipFailureAfterWriteCompensatesAndRollsBackDeletion(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{})
	want := errors.New("transaction failed after relationship write")
	service, err := campaign.NewService(campaign.Deps{Units: failedRelationshipUnits{Units: h.units, Err: want}, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Runtime: h.runtime, Clock: func() time.Time { return h.now }})
	require.NoError(t, err)
	require.ErrorIs(t, service.DeleteCampaign(h.ctx, campaign.DeleteCampaignCommand{ID: row.ID}), want)
	stored, ok := h.store.Campaign(row.ID)
	require.True(t, ok)
	require.Equal(t, row, stored)
	require.Len(t, h.checker.Applied(), 1)
	require.Len(t, h.checker.Compensated(), 1)
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestPreviewUsesExactSubjectFallbackAndUnsavedLayout(t *testing.T) {
	h := newHarness(t)
	row := h.store.PutCampaign(campaign.Campaign{Subject: "Source {{recipient_email}}"})
	target := "Target {{recipient_email}}"
	h.store.PutLocale(row.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: "ko", Subject: &target, UpdatedAt: h.now}})
	locale := "ko"
	layoutID := uuid.NewString()
	called := false
	h.store.Wrap = func(id, gotLocale, _ string, data map[string]string) (string, error) {
		called = true
		require.Equal(t, layoutID, id)
		require.Equal(t, locale, gotLocale)
		require.Equal(t, "Target john@example.com", data["subject"])
		return "<p>wrapped</p>", nil
	}
	preview, err := h.service.PreviewCampaign(h.ctx, campaign.PreviewCampaignQuery{ID: row.ID, Locale: &locale, LayoutID: &layoutID})
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, "Target john@example.com", preview.Subject)
	require.Contains(t, preview.HTMLContent, "wrapped")
	require.Contains(t, preview.TextContent, "wrapped")
	unsaved := "Unsaved {{recipient_email}}"
	locale = "ja"
	preview, err = h.service.PreviewCampaign(h.ctx, campaign.PreviewCampaignQuery{ID: row.ID, Locale: &locale, Subject: &unsaved})
	require.NoError(t, err)
	require.Equal(t, "Unsaved john@example.com", preview.Subject)
	stored, _ := h.store.Campaign(row.ID)
	require.Equal(t, row, stored)
	require.Empty(t, h.audit.Records())
}
func TestTestSendRejectsInvalidAddressReadinessAndQueueFailure(t *testing.T) {
	for _, kind := range []string{"address", "locale", "readiness", "queue"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t)
			row := h.store.PutCampaign(campaign.Campaign{})
			command := campaign.SendTestCampaignCommand{ID: row.ID, Email: "member@example.test"}
			want := errors.New("mail unavailable")
			switch kind {
			case "address":
				command.Email = "invalid"
			case "locale":
				locale := "xx"
				command.Locale = &locale
			case "readiness":
				h.store.ReadinessError = want
			case "queue":
				h.store.Fail("Enqueue", want)
			}
			err := h.service.SendTestCampaign(h.ctx, command)
			if kind == "address" || kind == "locale" {
				require.Equal(t, connect.CodeInvalidArgument, code(err))
			} else {
				require.ErrorIs(t, err, want)
			}
			require.Empty(t, h.store.Queued())
			require.Empty(t, h.audit.Records())
			require.Equal(t, mutation.StepRollback, h.lastStep())
		})
	}
}
