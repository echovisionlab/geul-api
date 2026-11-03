package criteria_test

import (
	"cmp"
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/member/criteria/criteriatest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// archiveFixture is a criteria that scheduled work and download policies
// target, beside another criteria the policies keep.
type archiveFixture struct {
	criteria, other          membercriteria.Criteria
	scheduled, sendingRunsOf string // campaigns
	draft                    string
	scheduledRun, sendingRun string
	shared, single, track    membercriteria.DownloadPolicy
}

func seedArchiveFixture(store *criteriatest.Store) archiveFixture {
	f := archiveFixture{
		criteria: store.SeedCriteria(membercriteria.Criteria{Name: "Archived", Type: membercriteria.KindFilter}),
		other:    store.SeedCriteria(membercriteria.Criteria{Name: "Kept", Type: membercriteria.KindFilter}),
	}
	f.scheduled = store.SeedCampaign(f.criteria.ID, criteriatest.CampaignScheduled)
	f.sendingRunsOf = store.SeedCampaign(f.criteria.ID, criteriatest.CampaignScheduled)
	f.draft = store.SeedCampaign(f.criteria.ID, criteriatest.CampaignDraft)
	f.scheduledRun = store.SeedDeliveryRun(f.criteria.ID, criteriatest.RunScheduled)
	f.sendingRun = store.SeedDeliveryRun(f.criteria.ID, criteriatest.RunSending)
	policy := func(owner string, criteriaIDs ...string) membercriteria.DownloadPolicy {
		return store.SeedDownloadPolicy(membercriteria.DownloadPolicy{
			Owner: owner, OwnerID: uuid.NewString(), ItemID: uuid.NewString(), FileID: uuid.NewString(),
			Criteria: "restricted", CriteriaIDs: criteriaIDs,
		})
	}
	f.shared = policy("post", f.criteria.ID, f.other.ID)
	f.single = policy("post", f.criteria.ID)
	f.track = policy("release", f.criteria.ID, f.other.ID)
	return f
}

func TestArchiveCriteriaReleasesScheduledWorkAndDetachesDownloadPolicies(t *testing.T) {
	h := newHarness(t)
	f := seedArchiveFixture(h.store)

	archived, err := h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: f.criteria.ID})
	require.NoError(t, err)
	require.NotNil(t, archived.Criteria.ArchivedAt)
	require.Equal(t, membercriteria.References{Campaigns: 3, DeliveryRuns: 2}, archived.References)

	for _, campaignID := range []string{f.scheduled, f.sendingRunsOf, f.draft} {
		require.Equal(t, criteriatest.CampaignDraft, h.store.CampaignOf(campaignID).Status)
	}
	require.Equal(t, criteriatest.DeliveryRun{CriteriaID: f.criteria.ID, Status: criteriatest.RunCancelled, Completed: true}, h.store.DeliveryRunOf(f.scheduledRun))
	require.Equal(t, criteriatest.DeliveryRun{CriteriaID: f.criteria.ID, Status: criteriatest.RunSending}, h.store.DeliveryRunOf(f.sendingRun))
	require.Equal(t, []string{f.other.ID}, h.store.PolicyCriteriaIDs(f.shared.ItemID))
	require.Empty(t, h.store.PolicyCriteriaIDs(f.single.ItemID))
	require.Equal(t, []string{f.other.ID}, h.store.PolicyCriteriaIDs(f.track.ItemID))
	require.Equal(t, []string{"campaign usage", "criteria", "download policies"}, h.store.Locks(),
		"campaigns and runs lock before the criteria, the criteria before the exact policies")

	drafted := []string{f.scheduled, f.sendingRunsOf}
	slices.Sort(drafted)
	attachments := []membercriteria.DownloadPolicy{f.shared, f.single}
	slices.SortFunc(attachments, func(a, b membercriteria.DownloadPolicy) int { return cmp.Compare(a.ItemID, b.ItemID) })
	var want []auditRecord
	for _, campaignID := range drafted {
		want = append(want, auditRecord{
			Action: sharedtelemetry.AuditCampaignUpdated, TargetType: "campaign", TargetID: campaignID,
			ChangedFields: []string{"status"}, PreviousState: sharedtelemetry.AuditStateScheduled, NewState: sharedtelemetry.AuditStateDraft,
		})
	}
	for _, policy := range append(attachments, f.track) {
		action, targetType := sharedtelemetry.AuditPostUpdated, "post"
		if policy.Owner == "release" {
			action, targetType = sharedtelemetry.AuditReleaseUpdated, "release"
		}
		kept := slices.DeleteFunc(slices.Clone(policy.CriteriaIDs), func(id string) bool { return id == f.criteria.ID })
		want = append(want, auditRecord{
			Action: action, TargetType: targetType, TargetID: policy.OwnerID, ItemID: policy.ItemID, FileID: policy.FileID,
			ChangedFields: []string{"file_download_member_criteria_ids"}, PreviousItemIDs: policy.CriteriaIDs, ItemIDs: kept,
		})
	}
	want = append(want, auditRecord{
		Action: sharedtelemetry.AuditMemberCriteriaUpdated, TargetType: "member_criteria", TargetID: f.criteria.ID,
		ChangedFields: []string{"status"}, PreviousState: sharedtelemetry.AuditStateActive, NewState: sharedtelemetry.AuditStateArchived,
	})
	require.Equal(t, want, h.auditRecords())

	_, err = h.service.UpdateCriteria(h.admin, membercriteria.UpdateCriteriaCommand{ID: f.criteria.ID, Name: pointer("Still frozen")})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code(), "the sending run keeps the archived criteria frozen")
}

func TestArchiveCriteriaOfAnArchivedCriteriaChangesNothing(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Archived", Type: membercriteria.KindAllMembers, ArchivedAt: &archivedAt})
	campaignID := h.store.SeedCampaign(criteria.ID, criteriatest.CampaignScheduled)

	archived, err := h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: criteria.ID})
	require.NoError(t, err)
	require.True(t, archivedAt.Equal(*archived.Criteria.ArchivedAt))
	require.Equal(t, criteriatest.CampaignScheduled, h.store.CampaignOf(campaignID).Status)
	require.Empty(t, h.auditRecords())
	require.Equal(t, []string{"criteria"}, h.store.Locks())
	require.Equal(t, mutation.StepCommit, h.lastStep())
}

func TestArchiveCriteriaRollsBackEveryChangeWhenAuditFails(t *testing.T) {
	h := newHarness(t)
	f := seedArchiveFixture(h.store)
	auditDown := errors.New("audit unavailable")
	h.audit.Err = auditDown

	_, err := h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: f.criteria.ID})
	require.ErrorIs(t, err, auditDown)
	stored, _ := h.store.Stored(f.criteria.ID)
	require.Nil(t, stored.ArchivedAt)
	require.Equal(t, criteriatest.CampaignScheduled, h.store.CampaignOf(f.scheduled).Status)
	require.Equal(t, criteriatest.RunScheduled, h.store.DeliveryRunOf(f.scheduledRun).Status)
	require.Equal(t, []string{f.criteria.ID}, h.store.PolicyCriteriaIDs(f.single.ItemID))
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestArchiveCriteriaRetriesWhenCampaignUsageChangesWhileLocking(t *testing.T) {
	h := newHarness(t)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Raced", Type: membercriteria.KindFilter})
	raced := 0
	h.store.BeforeUsageCheck = func(store *criteriatest.Store) {
		if raced == 0 {
			raced++
			store.SeedCampaign(criteria.ID, criteriatest.CampaignScheduled)
		}
	}
	archived, err := h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: criteria.ID})
	require.NoError(t, err)
	require.NotNil(t, archived.Criteria.ArchivedAt)
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepRollback,
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
	}, h.units.Steps(), "the second attempt locks what the first missed")

	h = newHarness(t)
	criteria = h.store.SeedCriteria(membercriteria.Criteria{Name: "Always raced", Type: membercriteria.KindFilter})
	h.store.BeforeUsageCheck = func(store *criteriatest.Store) { store.SeedCampaign(criteria.ID, criteriatest.CampaignDraft) }
	_, err = h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: criteria.ID})
	require.Equal(t, connect.CodeAborted, errs.Wrap(err).Code())
	require.Equal(t, "Member criteria changed while it was being archived; retry", errs.Wrap(err).Message())
	stored, _ := h.store.Stored(criteria.ID)
	require.Nil(t, stored.ArchivedAt)
}

func TestArchiveCriteriaChecksAuthorityAfterItsLocks(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	f := seedArchiveFixture(h.store)
	_, err := h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: f.criteria.ID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	stored, _ := h.store.Stored(f.criteria.ID)
	require.Nil(t, stored.ArchivedAt)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())

	_, err = h.service.ArchiveCriteria(h.admin, membercriteria.ArchiveCriteriaCommand{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestRestoreCriteriaReactivatesAnArchivedCriteria(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC().Add(-time.Hour)
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Archived", Type: membercriteria.KindAllMembers, ArchivedAt: &archivedAt})
	restored, err := h.service.RestoreCriteria(h.admin, membercriteria.RestoreCriteriaCommand{ID: criteria.ID})
	require.NoError(t, err)
	require.Nil(t, restored.Criteria.ArchivedAt)
	require.Equal(t, []auditRecord{{
		Action: sharedtelemetry.AuditMemberCriteriaUpdated, TargetType: "member_criteria", TargetID: criteria.ID,
		ChangedFields: []string{"status"}, PreviousState: sharedtelemetry.AuditStateArchived, NewState: sharedtelemetry.AuditStateActive,
	}}, h.auditRecords())

	_, err = h.service.RestoreCriteria(h.admin, membercriteria.RestoreCriteriaCommand{ID: criteria.ID})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.Equal(t, "Member criteria is not archived", errs.Wrap(err).Message())
	require.Len(t, h.auditRecords(), 1)
}

func TestRestoreCriteriaChecksAuthorityAfterTheRootLock(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	archivedAt := time.Now().UTC()
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Archived", Type: membercriteria.KindAllMembers, ArchivedAt: &archivedAt})
	_, err := h.service.RestoreCriteria(h.admin, membercriteria.RestoreCriteriaCommand{ID: criteria.ID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	stored, _ := h.store.Stored(criteria.ID)
	require.NotNil(t, stored.ArchivedAt)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
}
