package audience_test

import (
	"cmp"
	"errors"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/audience/audiencetest"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// archiveFixture is a segment that scheduled work and download policies
// target, beside another segment the policies keep.
type archiveFixture struct {
	segment, other           audience.Segment
	scheduled, sendingRunsOf string // campaigns
	draft                    string
	scheduledRun, sendingRun string
	shared, single, track    audience.DownloadPolicy
}

func seedArchiveFixture(store *audiencetest.Store) archiveFixture {
	f := archiveFixture{
		segment: store.SeedSegment(audience.Segment{Name: "Archived", Type: audience.SegmentMembersByFilter}),
		other:   store.SeedSegment(audience.Segment{Name: "Kept", Type: audience.SegmentMembersByFilter}),
	}
	f.scheduled = store.SeedCampaign(f.segment.ID, audiencetest.CampaignScheduled)
	f.sendingRunsOf = store.SeedCampaign(f.segment.ID, audiencetest.CampaignScheduled)
	f.draft = store.SeedCampaign(f.segment.ID, audiencetest.CampaignDraft)
	f.scheduledRun = store.SeedDeliveryRun(f.segment.ID, audiencetest.RunScheduled)
	f.sendingRun = store.SeedDeliveryRun(f.segment.ID, audiencetest.RunSending)
	policy := func(owner string, segmentIDs ...string) audience.DownloadPolicy {
		return store.SeedDownloadPolicy(audience.DownloadPolicy{
			Owner: owner, OwnerID: uuid.NewString(), ItemID: uuid.NewString(), FileID: uuid.NewString(),
			Audience: "restricted", SegmentIDs: segmentIDs,
		})
	}
	f.shared = policy("post", f.segment.ID, f.other.ID)
	f.single = policy("post", f.segment.ID)
	f.track = policy("release", f.segment.ID, f.other.ID)
	return f
}

func TestArchiveSegmentReleasesScheduledWorkAndDetachesDownloadPolicies(t *testing.T) {
	h := newHarness(t)
	f := seedArchiveFixture(h.store)

	archived, err := h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: f.segment.ID})
	require.NoError(t, err)
	require.NotNil(t, archived.Segment.ArchivedAt)
	require.Equal(t, audience.References{Campaigns: 3, DeliveryRuns: 2}, archived.References)

	for _, campaignID := range []string{f.scheduled, f.sendingRunsOf, f.draft} {
		require.Equal(t, audiencetest.CampaignDraft, h.store.CampaignOf(campaignID).Status)
	}
	require.Equal(t, audiencetest.DeliveryRun{SegmentID: f.segment.ID, Status: audiencetest.RunCancelled, Completed: true}, h.store.DeliveryRunOf(f.scheduledRun))
	require.Equal(t, audiencetest.DeliveryRun{SegmentID: f.segment.ID, Status: audiencetest.RunSending}, h.store.DeliveryRunOf(f.sendingRun))
	require.Equal(t, []string{f.other.ID}, h.store.PolicySegmentIDs(f.shared.ItemID))
	require.Empty(t, h.store.PolicySegmentIDs(f.single.ItemID))
	require.Equal(t, []string{f.other.ID}, h.store.PolicySegmentIDs(f.track.ItemID))
	require.Equal(t, []string{"campaign usage", "segment", "download policies"}, h.store.Locks(),
		"campaigns and runs lock before the segment, the segment before the exact policies")

	drafted := []string{f.scheduled, f.sendingRunsOf}
	slices.Sort(drafted)
	attachments := []audience.DownloadPolicy{f.shared, f.single}
	slices.SortFunc(attachments, func(a, b audience.DownloadPolicy) int { return cmp.Compare(a.ItemID, b.ItemID) })
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
		kept := slices.DeleteFunc(slices.Clone(policy.SegmentIDs), func(id string) bool { return id == f.segment.ID })
		want = append(want, auditRecord{
			Action: action, TargetType: targetType, TargetID: policy.OwnerID, ItemID: policy.ItemID, FileID: policy.FileID,
			ChangedFields: []string{"file_download_audience_segment_ids"}, PreviousItemIDs: policy.SegmentIDs, ItemIDs: kept,
		})
	}
	want = append(want, auditRecord{
		Action: sharedtelemetry.AuditAudienceSegmentUpdated, TargetType: "audience_segment", TargetID: f.segment.ID,
		ChangedFields: []string{"status"}, PreviousState: sharedtelemetry.AuditStateActive, NewState: sharedtelemetry.AuditStateArchived,
	})
	require.Equal(t, want, h.auditRecords())

	_, err = h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{ID: f.segment.ID, Name: pointer("Still frozen")})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code(), "the sending run keeps the archived segment frozen")
}

func TestArchiveSegmentOfAnArchivedSegmentChangesNothing(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	segment := h.store.SeedSegment(audience.Segment{Name: "Archived", Type: audience.SegmentAllMembers, ArchivedAt: &archivedAt})
	campaignID := h.store.SeedCampaign(segment.ID, audiencetest.CampaignScheduled)

	archived, err := h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: segment.ID})
	require.NoError(t, err)
	require.True(t, archivedAt.Equal(*archived.Segment.ArchivedAt))
	require.Equal(t, audiencetest.CampaignScheduled, h.store.CampaignOf(campaignID).Status)
	require.Empty(t, h.auditRecords())
	require.Equal(t, []string{"segment"}, h.store.Locks())
	require.Equal(t, mutation.StepCommit, h.lastStep())
}

func TestArchiveSegmentRollsBackEveryChangeWhenAuditFails(t *testing.T) {
	h := newHarness(t)
	f := seedArchiveFixture(h.store)
	auditDown := errors.New("audit unavailable")
	h.audit.Err = auditDown

	_, err := h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: f.segment.ID})
	require.ErrorIs(t, err, auditDown)
	stored, _ := h.store.Stored(f.segment.ID)
	require.Nil(t, stored.ArchivedAt)
	require.Equal(t, audiencetest.CampaignScheduled, h.store.CampaignOf(f.scheduled).Status)
	require.Equal(t, audiencetest.RunScheduled, h.store.DeliveryRunOf(f.scheduledRun).Status)
	require.Equal(t, []string{f.segment.ID}, h.store.PolicySegmentIDs(f.single.ItemID))
	require.Equal(t, mutation.StepRollback, h.lastStep())
}

func TestArchiveSegmentRetriesWhenCampaignUsageChangesWhileLocking(t *testing.T) {
	h := newHarness(t)
	segment := h.store.SeedSegment(audience.Segment{Name: "Raced", Type: audience.SegmentMembersByFilter})
	raced := 0
	h.store.BeforeUsageCheck = func(store *audiencetest.Store) {
		if raced == 0 {
			raced++
			store.SeedCampaign(segment.ID, audiencetest.CampaignScheduled)
		}
	}
	archived, err := h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: segment.ID})
	require.NoError(t, err)
	require.NotNil(t, archived.Segment.ArchivedAt)
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepRollback,
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
	}, h.units.Steps(), "the second attempt locks what the first missed")

	h = newHarness(t)
	segment = h.store.SeedSegment(audience.Segment{Name: "Always raced", Type: audience.SegmentMembersByFilter})
	h.store.BeforeUsageCheck = func(store *audiencetest.Store) { store.SeedCampaign(segment.ID, audiencetest.CampaignDraft) }
	_, err = h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: segment.ID})
	require.Equal(t, connect.CodeAborted, errs.Wrap(err).Code())
	require.Equal(t, "audience segment changed while it was being archived; retry", errs.Wrap(err).Message())
	stored, _ := h.store.Stored(segment.ID)
	require.Nil(t, stored.ArchivedAt)
}

func TestArchiveSegmentChecksAuthorityAfterItsLocks(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	f := seedArchiveFixture(h.store)
	_, err := h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: f.segment.ID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	stored, _ := h.store.Stored(f.segment.ID)
	require.Nil(t, stored.ArchivedAt)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())

	_, err = h.service.ArchiveSegment(h.admin, audience.ArchiveSegmentCommand{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestRestoreSegmentReactivatesAnArchivedSegment(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC().Add(-time.Hour)
	segment := h.store.SeedSegment(audience.Segment{Name: "Archived", Type: audience.SegmentAllMembers, ArchivedAt: &archivedAt})
	restored, err := h.service.RestoreSegment(h.admin, audience.RestoreSegmentCommand{ID: segment.ID})
	require.NoError(t, err)
	require.Nil(t, restored.Segment.ArchivedAt)
	require.Equal(t, []auditRecord{{
		Action: sharedtelemetry.AuditAudienceSegmentUpdated, TargetType: "audience_segment", TargetID: segment.ID,
		ChangedFields: []string{"status"}, PreviousState: sharedtelemetry.AuditStateArchived, NewState: sharedtelemetry.AuditStateActive,
	}}, h.auditRecords())

	_, err = h.service.RestoreSegment(h.admin, audience.RestoreSegmentCommand{ID: segment.ID})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.Equal(t, "audience segment is not archived", errs.Wrap(err).Message())
	require.Len(t, h.auditRecords(), 1)
}

func TestRestoreSegmentChecksAuthorityAfterTheRootLock(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	archivedAt := time.Now().UTC()
	segment := h.store.SeedSegment(audience.Segment{Name: "Archived", Type: audience.SegmentAllMembers, ArchivedAt: &archivedAt})
	_, err := h.service.RestoreSegment(h.admin, audience.RestoreSegmentCommand{ID: segment.ID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	stored, _ := h.store.Stored(segment.ID)
	require.NotNil(t, stored.ArchivedAt)
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
}
