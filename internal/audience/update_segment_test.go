package audience_test

import (
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
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestUpdateSegmentAuditsTheChangedFieldsOnly(t *testing.T) {
	h := newHarness(t)
	segment := h.store.SeedSegment(audience.Segment{Name: "Before", Type: audience.SegmentMembersByFilter})
	updated, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{ID: segment.ID, Name: pointer("After")})
	require.NoError(t, err)
	require.Equal(t, "After", updated.Segment.Name)
	require.True(t, updated.Segment.UpdatedAt.After(*segment.UpdatedAt) || updated.Segment.UpdatedAt.Equal(*segment.UpdatedAt))
	require.Equal(t, []auditRecord{{
		Action: sharedtelemetry.AuditAudienceSegmentUpdated, TargetType: "audience_segment", TargetID: segment.ID, ChangedFields: []string{"name"},
	}}, h.auditRecords())
	require.Equal(t, []mutation.Step{
		mutation.StepLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit, mutation.StepCommit,
	}, h.units.Steps())

	unchanged, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID: segment.ID, Name: pointer("After"),
		Config: &audience.SegmentConfigInput{},
		Observed: &audience.ObservedSegment{SegmentType: audience.SegmentMembersByFilter, Config: &audience.SegmentConfigInput{}},
	})
	require.NoError(t, err)
	require.Equal(t, updated.Segment.UpdatedAt, unchanged.Segment.UpdatedAt, "a no-op writes nothing")
	require.Len(t, h.auditRecords(), 1, "a no-op appends no record")
}

func TestUpdateSegmentMissingSegmentAnswersNotFoundBeforeAuthorization(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	_, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{ID: uuid.NewString(), Name: pointer("x")})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	require.Equal(t, "segment not found", errs.Wrap(err).Message())
	require.Empty(t, h.checker.Checks())
}

func TestUpdateSegmentDeniedLeavesTheSegment(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	segment := h.store.SeedSegment(audience.Segment{Name: "Kept", Type: audience.SegmentAllMembers})
	_, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{ID: segment.ID, Name: pointer("Changed")})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	stored, _ := h.store.Stored(segment.ID)
	require.Equal(t, "Kept", stored.Name)
	editCan, err := policyv1.AudienceSegment.Edit(segment.ID)
	require.Equal(t, []authztest.Check{authztest.Of(h.identityID, editCan, err)}, h.checker.Checks())
	require.Empty(t, h.auditRecords())
}

func TestUpdateSegmentTypeSwitchWithoutConfigClearsTheObservedFilter(t *testing.T) {
	h := newHarness(t)
	tagID := h.store.SeedMemberTag()
	segment := h.store.SeedSegment(audience.Segment{Name: "Tagged", Type: audience.SegmentMemberTags, Config: audience.Config{MemberTagIDs: []string{tagID}}})
	desired := audience.SegmentAllMembers
	updated, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID: segment.ID, SegmentType: &desired,
		Observed: &audience.ObservedSegment{SegmentType: audience.SegmentMemberTags, Config: &audience.SegmentConfigInput{MemberTagIDs: []string{tagID}}},
	})
	require.NoError(t, err)
	require.Equal(t, desired, updated.Segment.Type)
	require.Empty(t, updated.Segment.Config.MemberTagIDs)
	stored, _ := h.store.Stored(segment.ID)
	require.Empty(t, stored.Config.MemberTagIDs)
	require.Equal(t, []string{"member_tag_ids", "segment_type"}, h.auditRecords()[0].ChangedFields)
}

func TestUpdateSegmentMergesTheFilterAgainstWhatTheEditorObserved(t *testing.T) {
	h := newHarness(t)
	tagA, tagB, peerTag := h.store.SeedMemberTag(), h.store.SeedMemberTag(), h.store.SeedMemberTag()
	segment := h.store.SeedSegment(audience.Segment{Name: "Merged", Type: audience.SegmentMemberTags, Config: audience.Config{
		MemberTagIDs: []string{tagA, peerTag},
	}})
	updated, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID:     segment.ID,
		Config: &audience.SegmentConfigInput{MemberTagIDs: []string{tagB}},
		Observed: &audience.ObservedSegment{
			SegmentType: audience.SegmentMemberTags, Config: &audience.SegmentConfigInput{MemberTagIDs: []string{tagA}},
		},
	})
	require.NoError(t, err)
	want := []string{tagB, peerTag}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	require.Equal(t, want, updated.Segment.Config.MemberTagIDs, "the peer's tag survives the editor's swap")
}

func TestUpdateSegmentRequiresTheObservedStateForAFilterChange(t *testing.T) {
	h := newHarness(t)
	segment := h.store.SeedSegment(audience.Segment{Name: "x", Type: audience.SegmentMembersByFilter})
	_, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{ID: segment.ID, Config: &audience.SegmentConfigInput{}})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Empty(t, h.units.Steps())
}

func TestUpdateSegmentIsFrozenByActiveDelivery(t *testing.T) {
	for _, test := range []struct {
		name    string
		seed    func(store *audiencetest.Store, segmentID string)
		message string
	}{
		{"scheduled campaign", func(store *audiencetest.Store, segmentID string) {
			store.SeedCampaign(segmentID, audiencetest.CampaignScheduled)
		}, "audience segment is used by a scheduled or sending campaign"},
		{"sending delivery run", func(store *audiencetest.Store, segmentID string) {
			store.SeedCampaign(segmentID, audiencetest.CampaignDraft)
			store.SeedDeliveryRun(segmentID, audiencetest.RunSending)
		}, "audience segment is frozen by an active delivery run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			segment := h.store.SeedSegment(audience.Segment{Name: "Frozen", Type: audience.SegmentAllMembers})
			test.seed(h.store, segment.ID)
			_, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{ID: segment.ID, Name: pointer("Changed")})
			require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
			require.Equal(t, test.message, errs.Wrap(err).Message())
			stored, _ := h.store.Stored(segment.ID)
			require.Equal(t, "Frozen", stored.Name)
		})
	}
}

func TestUpdateSegmentKeepsADownloadGrantingSegmentAnAccessType(t *testing.T) {
	h := newHarness(t)
	segment := h.store.SeedSegment(audience.Segment{Name: "Granting", Type: audience.SegmentMembersByFilter})
	h.store.SeedDownloadPolicy(audience.DownloadPolicy{Owner: "post", ItemID: uuid.NewString(), SegmentIDs: []string{segment.ID}})
	desired := audience.SegmentAllMembers
	_, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID: segment.ID, SegmentType: &desired,
		Observed: &audience.ObservedSegment{SegmentType: audience.SegmentMembersByFilter, Config: &audience.SegmentConfigInput{}},
	})
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	require.Equal(t, "audience segment grants download access and cannot change to this type", errs.Wrap(err).Message())

	desired = audience.SegmentMemberTags
	tagID := h.store.SeedMemberTag()
	_, err = h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID: segment.ID, SegmentType: &desired, Config: &audience.SegmentConfigInput{MemberTagIDs: []string{tagID}},
		Observed: &audience.ObservedSegment{SegmentType: audience.SegmentMembersByFilter, Config: &audience.SegmentConfigInput{}},
	})
	require.NoError(t, err, "another access type keeps the grant")
}

func TestUpdateSegmentOfAnArchivedSegmentChangesMetadataAndFilter(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC().Add(-time.Hour)
	segment := h.store.SeedSegment(audience.Segment{Name: "Archived", Type: audience.SegmentMembersByFilter, ArchivedAt: &archivedAt})
	after := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	updated, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID: segment.ID, Name: pointer("Renamed"), Description: pointer("Described"),
		Config:   &audience.SegmentConfigInput{AccountRoles: []string{policyv1.Role.Admin().ID()}, CreatedAfter: &after},
		Observed: &audience.ObservedSegment{SegmentType: audience.SegmentMembersByFilter, Config: &audience.SegmentConfigInput{}},
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Segment.ArchivedAt)
	require.Equal(t, "Renamed", updated.Segment.Name)
	require.Equal(t, "Described", *updated.Segment.Description)
	require.Equal(t, []string{policyv1.Role.Admin().ID()}, updated.Segment.Config.AccountRoles)
	require.True(t, after.Truncate(time.Microsecond).Equal(*updated.Segment.Config.CreatedAfter), "the response reads the stored window back")
	require.Equal(t, []string{"account_roles", "created_after", "description", "name"}, h.auditRecords()[0].ChangedFields)
}

func TestUpdateSegmentRejectsUnknownReferences(t *testing.T) {
	h := newHarness(t)
	tagged := h.store.SeedSegment(audience.Segment{Name: "y", Type: audience.SegmentMemberTags, Config: audience.Config{MemberTagIDs: []string{h.store.SeedMemberTag()}}})
	_, err := h.service.UpdateSegment(h.admin, audience.UpdateSegmentCommand{
		ID: tagged.ID, Config: &audience.SegmentConfigInput{MemberTagIDs: []string{uuid.NewString()}},
		Observed: &audience.ObservedSegment{SegmentType: audience.SegmentMemberTags, Config: &audience.SegmentConfigInput{MemberTagIDs: tagged.Config.MemberTagIDs}},
	})
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, "one or more audience references do not exist", errs.Wrap(err).Message())
	stored, _ := h.store.Stored(tagged.ID)
	require.Equal(t, tagged.Config.MemberTagIDs, stored.Config.MemberTagIDs)
	require.Equal(t, mutation.StepRollback, h.lastStep())
}
