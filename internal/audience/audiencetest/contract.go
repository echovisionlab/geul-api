package audiencetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/audience"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Fixtures seeds the rows a Store reads but other domains write: Member
// tags, Members, campaigns, delivery runs and download policies.
type Fixtures interface {
	MemberTag(t *testing.T) string
	// Member stores a Member, onboarded with an exact Identity link when
	// eligible.
	Member(t *testing.T, eligible bool) string
	Campaign(t *testing.T, segmentID, status string) string
	DeliveryRun(t *testing.T, campaignID, segmentID, status string) string
	// AttachmentPolicy stores a Post's restricted File attachment that grants
	// through segmentIDs; TrackPolicy a Release's restricted Track.
	AttachmentPolicy(t *testing.T, segmentIDs ...string) audience.DownloadPolicy
	TrackPolicy(t *testing.T, segmentIDs ...string) audience.DownloadPolicy
	CampaignOf(t *testing.T, id string) Campaign
	DeliveryRunOf(t *testing.T, id string) DeliveryRun
}

// Harness is one Store implementation and its fixtures.
type Harness struct {
	mutationtest.Harness[audience.Store]
	Fixtures
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store, _ := New()
	return Harness{Harness: mutationtest.FakeHarness[audience.Store](store), Fixtures: fakeFixtures{store}}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel, as mutationtest.RunContract does; the cases also need the
// Harness's fixtures.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

type fakeFixtures struct{ store *Store }

func (f fakeFixtures) MemberTag(*testing.T) string               { return f.store.SeedMemberTag() }
func (f fakeFixtures) Member(_ *testing.T, eligible bool) string { return f.store.SeedMember(eligible) }
func (f fakeFixtures) Campaign(_ *testing.T, segmentID, status string) string {
	return f.store.SeedCampaign(segmentID, status)
}
func (f fakeFixtures) DeliveryRun(_ *testing.T, _, segmentID, status string) string {
	return f.store.SeedDeliveryRun(segmentID, status)
}
func (f fakeFixtures) AttachmentPolicy(_ *testing.T, segmentIDs ...string) audience.DownloadPolicy {
	return f.store.SeedDownloadPolicy(audience.DownloadPolicy{
		Owner: "post", OwnerID: uuid.NewString(), ItemID: uuid.NewString(), FileID: uuid.NewString(),
		Audience: "restricted", SegmentIDs: segmentIDs,
	})
}
func (f fakeFixtures) TrackPolicy(_ *testing.T, segmentIDs ...string) audience.DownloadPolicy {
	return f.store.SeedDownloadPolicy(audience.DownloadPolicy{
		Owner: "release", OwnerID: uuid.NewString(), ItemID: uuid.NewString(), FileID: uuid.NewString(),
		Audience: "restricted", SegmentIDs: segmentIDs,
	})
}
func (f fakeFixtures) CampaignOf(_ *testing.T, id string) Campaign { return f.store.CampaignOf(id) }
func (f fakeFixtures) DeliveryRunOf(_ *testing.T, id string) DeliveryRun {
	return f.store.DeliveryRunOf(id)
}

// insert stores a segment of segmentType with config through the Store.
func insert(t *testing.T, h Harness, name string, segmentType audience.SegmentType, config audience.Config) audience.Segment {
	t.Helper()
	var created audience.Segment
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store audience.Store) (err error) {
		created, err = store.InsertSegment(ctx, audience.Segment{Name: name, Type: segmentType, Config: config})
		if err != nil {
			return err
		}
		return store.ReplaceConfig(ctx, created.ID, config)
	}))
	return created
}

func read(t *testing.T, h Harness, id string) audience.Segment {
	t.Helper()
	var segment audience.Segment
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) (err error) {
		segment, err = store.Segment(ctx, id)
		return err
	}))
	return segment
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store audience.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func list(t *testing.T, h Harness, includeArchived bool, page audience.Page) ([]string, int64) {
	t.Helper()
	var segments []audience.Segment
	var total int64
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) (err error) {
		segments, total, err = store.ListSegments(ctx, includeArchived, page)
		return err
	}))
	names := make([]string, len(segments))
	for i, segment := range segments {
		names[i] = segment.Name
	}
	return names, total
}

// storeContract is the behavior every audience.Store has.
var storeContract = map[string]func(t *testing.T, h Harness){
	"an inserted segment reads back with its ID, timestamps and filter at microsecond precision": func(t *testing.T, h Harness) {
		tags := []string{h.MemberTag(t), h.MemberTag(t)}
		member := h.Member(t, true)
		after := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.UTC)
		description := "Described"
		config := audience.Config{
			MemberTagIDs: tags, AccountRoles: []string{policyv1.Role.Admin().ID(), policyv1.Role.User().ID()},
			CreatedAfter: &after, ExcludeMemberIDs: []string{member},
		}
		var created audience.Segment
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) (err error) {
			created, err = store.InsertSegment(ctx, audience.Segment{
				Name: "Inserted", Description: &description, Type: audience.SegmentMembersByFilter, Config: config,
			})
			if err != nil {
				return err
			}
			return store.ReplaceConfig(ctx, created.ID, config)
		}))
		require.NotEmpty(t, created.ID)
		require.False(t, created.CreatedAt.IsZero())
		require.NotNil(t, created.UpdatedAt)
		require.Nil(t, created.ArchivedAt)
		require.Equal(t, after, *created.Config.CreatedAfter, "the inserted segment carries the filter as given")

		stored := read(t, h, created.ID)
		require.Equal(t, "Inserted", stored.Name)
		require.Equal(t, description, *stored.Description)
		require.Equal(t, audience.SegmentMembersByFilter, stored.Type)
		require.ElementsMatch(t, tags, stored.Config.MemberTagIDs)
		require.IsIncreasing(t, stored.Config.MemberTagIDs)
		require.Equal(t, config.AccountRoles, stored.Config.AccountRoles)
		require.Equal(t, []string{member}, stored.Config.ExcludeMemberIDs)
		require.True(t, after.Truncate(time.Microsecond).Equal(*stored.Config.CreatedAfter))
		require.Nil(t, stored.Config.CreatedBefore)
		require.True(t, created.CreatedAt.Equal(stored.CreatedAt))

		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			locked, err := store.LockSegment(ctx, created.ID)
			require.Equal(t, stored.Config, locked.Config)
			if err != nil {
				return err
			}
			archived, err := store.SegmentArchived(ctx, created.ID)
			require.False(t, archived)
			return err
		}))
	},
	"a missing segment is not found": func(t *testing.T, h Harness) {
		id := uuid.NewString()
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) error {
			_, err := store.Segment(ctx, id)
			return err
		}), audience.ErrSegmentNotFound)
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			_, err := store.LockSegment(ctx, id)
			return err
		}), audience.ErrSegmentNotFound)
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			_, err := store.SegmentArchived(ctx, id)
			return err
		}), audience.ErrSegmentNotFound)
	},
	"a filter naming a missing Member tag or Member is an unknown reference": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Unknown references", audience.SegmentMembersByFilter, audience.Config{})
		for name, config := range map[string]audience.Config{
			"tag":    {MemberTagIDs: []string{uuid.NewString()}},
			"member": {ExcludeMemberIDs: []string{uuid.NewString()}},
		} {
			err := transact(t, h, func(ctx context.Context, store audience.Store) error {
				return store.ReplaceConfig(ctx, segment.ID, config)
			})
			require.ErrorIs(t, err, audience.ErrUnknownReference, name)
		}
	},
	"replacing a filter drops the relations it no longer names": func(t *testing.T, h Harness) {
		kept, dropped := h.MemberTag(t), h.MemberTag(t)
		member := h.Member(t, true)
		segment := insert(t, h, "Replaced", audience.SegmentMembersByFilter, audience.Config{
			MemberTagIDs: []string{kept, dropped}, AccountRoles: []string{policyv1.Role.Author().ID()}, ExcludeMemberIDs: []string{member},
		})
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.ReplaceConfig(ctx, segment.ID, audience.Config{MemberTagIDs: []string{kept}})
		}))
		stored := read(t, h, segment.ID)
		require.Equal(t, []string{kept}, stored.Config.MemberTagIDs)
		require.Empty(t, stored.Config.AccountRoles)
		require.Empty(t, stored.Config.ExcludeMemberIDs)
	},
	"an update changes only the columns it names": func(t *testing.T, h Harness) {
		after := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
		segment := insert(t, h, "Before update", audience.SegmentMembersByFilter, audience.Config{CreatedAfter: &after})
		name := "After update"
		updatedAt := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.UpdateSegment(ctx, segment.ID, audience.SegmentChange{Name: &name, SetCreatedAfter: true, UpdatedAt: updatedAt})
		}))
		stored := read(t, h, segment.ID)
		require.Equal(t, name, stored.Name)
		require.Nil(t, stored.Description)
		require.Nil(t, stored.Config.CreatedAfter, "a set time without a value clears it")
		require.Equal(t, audience.SegmentMembersByFilter, stored.Type)
		require.True(t, updatedAt.Equal(*stored.UpdatedAt))

		description, segmentType := "Described", audience.SegmentAllMembers
		before := time.Date(2026, time.May, 1, 2, 3, 4, 567891234, time.UTC)
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.UpdateSegment(ctx, segment.ID, audience.SegmentChange{
				Description: &description, Type: &segmentType, SetCreatedBefore: true, CreatedBefore: &before, UpdatedAt: updatedAt,
			})
		}))
		stored = read(t, h, segment.ID)
		require.Equal(t, name, stored.Name)
		require.Equal(t, description, *stored.Description)
		require.Equal(t, segmentType, stored.Type)
		require.True(t, before.Truncate(time.Microsecond).Equal(*stored.Config.CreatedBefore))
	},
	"archive and restore apply only from the opposite state": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Lifecycle", audience.SegmentMembersByFilter, audience.Config{})
		archivedAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.ArchiveSegment(ctx, segment.ID, archivedAt)
		}))
		stored := read(t, h, segment.ID)
		require.True(t, archivedAt.Equal(*stored.ArchivedAt))
		require.True(t, archivedAt.Equal(*stored.UpdatedAt))
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.ArchiveSegment(ctx, segment.ID, archivedAt)
		}), audience.ErrArchiveStateChanged)
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			archived, err := store.SegmentArchived(ctx, segment.ID)
			require.True(t, archived)
			return err
		}))

		restoredAt := archivedAt.Add(time.Hour)
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.RestoreSegment(ctx, segment.ID, restoredAt)
		}))
		stored = read(t, h, segment.ID)
		require.Nil(t, stored.ArchivedAt)
		require.True(t, restoredAt.Equal(*stored.UpdatedAt))
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.RestoreSegment(ctx, segment.ID, restoredAt)
		}), audience.ErrArchiveStateChanged)
	},
	"a list holds active segments unless asked, in name and ID order, with the total": func(t *testing.T, h Harness) {
		tag := h.MemberTag(t)
		insert(t, h, "Beta", audience.SegmentMemberTags, audience.Config{MemberTagIDs: []string{tag}})
		insert(t, h, "Alpha", audience.SegmentAllMembers, audience.Config{})
		gamma := insert(t, h, "Gamma", audience.SegmentMembersByFilter, audience.Config{})
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.ArchiveSegment(ctx, gamma.ID, time.Now().UTC())
		}))

		names, total := list(t, h, false, audience.Page{Limit: 20})
		require.Equal(t, []string{"Alpha", "Beta"}, names)
		require.EqualValues(t, 2, total)
		names, total = list(t, h, true, audience.Page{Limit: 20})
		require.Equal(t, []string{"Alpha", "Beta", "Gamma"}, names)
		require.EqualValues(t, 3, total)
		names, total = list(t, h, true, audience.Page{Limit: 1, Offset: 1})
		require.Equal(t, []string{"Beta"}, names)
		require.EqualValues(t, 3, total)
		names, total = list(t, h, true, audience.Page{Limit: 20, Sorts: []*commonv1.SortSpec{{Field: "name", Order: commonv1.SortOrder_SORT_ORDER_DESC}}})
		require.Equal(t, []string{"Gamma", "Beta", "Alpha"}, names)
		require.EqualValues(t, 3, total)
		names, total = list(t, h, true, audience.Page{Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "ALP"}}})
		require.Equal(t, []string{"Alpha"}, names)
		require.EqualValues(t, 1, total)

		var segments []audience.Segment
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) (err error) {
			segments, _, err = store.ListSegments(ctx, false, audience.Page{Limit: 20})
			return err
		}))
		require.Equal(t, []string{tag}, segments[1].Config.MemberTagIDs, "a listed segment carries its filter")

		for name, page := range map[string]audience.Page{
			"filter field": {Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "name", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "a"}}},
			"filter op":    {Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "a"}}},
			"sort field":   {Limit: 20, Sorts: []*commonv1.SortSpec{{Field: "id"}}},
		} {
			err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) error {
				_, _, err := store.ListSegments(ctx, true, page)
				return err
			})
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code(), name)
		}
	},
	"the authenticated access list holds active member-tag and member-filter segments without filters": func(t *testing.T, h Harness) {
		tag := h.MemberTag(t)
		insert(t, h, "All members", audience.SegmentAllMembers, audience.Config{})
		insert(t, h, "Tagged", audience.SegmentMemberTags, audience.Config{MemberTagIDs: []string{tag}})
		insert(t, h, "Filtered", audience.SegmentMembersByFilter, audience.Config{AccountRoles: []string{policyv1.Role.User().ID()}})
		archived := insert(t, h, "Archived filter", audience.SegmentMembersByFilter, audience.Config{})
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			return store.ArchiveSegment(ctx, archived.ID, time.Now().UTC())
		}))
		var segments []audience.Segment
		var total int64
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) (err error) {
			segments, total, err = store.ListAuthenticatedAccessSegments(ctx, audience.Page{Limit: 20})
			return err
		}))
		require.EqualValues(t, 2, total)
		require.Len(t, segments, 2)
		require.Equal(t, "Filtered", segments[0].Name)
		require.Equal(t, "Tagged", segments[1].Name)
		for _, segment := range segments {
			require.Empty(t, segment.Config.MemberTagIDs)
			require.Empty(t, segment.Config.AccountRoles)
		}
	},
	"references count campaigns, delivery runs and download policies": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Referenced", audience.SegmentMembersByFilter, audience.Config{})
		other := insert(t, h, "Other", audience.SegmentMembersByFilter, audience.Config{})
		unused := insert(t, h, "Unused", audience.SegmentMembersByFilter, audience.Config{})
		h.Campaign(t, segment.ID, CampaignDraft)
		scheduled := h.Campaign(t, segment.ID, CampaignScheduled)
		h.DeliveryRun(t, scheduled, segment.ID, RunScheduled)
		h.AttachmentPolicy(t, segment.ID)
		h.TrackPolicy(t, segment.ID, other.ID)
		var references map[string]audience.References
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) (err error) {
			references, err = store.SegmentReferences(ctx, []string{segment.ID, other.ID, unused.ID})
			return err
		}))
		require.Equal(t, map[string]audience.References{
			segment.ID: {Campaigns: 2, DeliveryRuns: 1, DownloadPolicies: 2},
			other.ID:   {DownloadPolicies: 1},
		}, references)
	},
	"active delivery is a scheduled or sending campaign or delivery run": func(t *testing.T, h Harness) {
		idle := insert(t, h, "Idle", audience.SegmentAllMembers, audience.Config{})
		h.Campaign(t, idle.ID, CampaignDraft)
		scheduled := insert(t, h, "Scheduled", audience.SegmentAllMembers, audience.Config{})
		h.Campaign(t, scheduled.ID, CampaignScheduled)
		running := insert(t, h, "Running", audience.SegmentAllMembers, audience.Config{})
		h.DeliveryRun(t, h.Campaign(t, running.ID, CampaignDraft), running.ID, RunSending)
		for segmentID, want := range map[string][2]bool{idle.ID: {false, false}, scheduled.ID: {true, false}, running.ID: {false, true}} {
			require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
				campaign, run, err := store.SegmentInActiveDelivery(ctx, segmentID)
				require.Equal(t, want, [2]bool{campaign, run})
				return err
			}))
		}
	},
	"releasing a segment drafts its scheduled campaigns and cancels only its scheduled runs": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Released", audience.SegmentMembersByFilter, audience.Config{})
		first := h.Campaign(t, segment.ID, CampaignScheduled)
		second := h.Campaign(t, segment.ID, CampaignScheduled)
		draft := h.Campaign(t, segment.ID, CampaignDraft)
		scheduledRun := h.DeliveryRun(t, first, segment.ID, RunScheduled)
		sendingRun := h.DeliveryRun(t, second, segment.ID, RunSending)

		var locked audience.CampaignUsage
		var drafted []string
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) (err error) {
			if locked, err = store.LockCampaignUsage(ctx, segment.ID); err != nil {
				return err
			}
			changed, err := store.CampaignUsageChanged(ctx, segment.ID, locked)
			require.False(t, changed)
			if err != nil {
				return err
			}
			drafted, err = store.ReleaseScheduledCampaigns(ctx, segment.ID, time.Now().UTC())
			return err
		}))
		require.ElementsMatch(t, []string{first, second, draft}, locked.CampaignIDs)
		require.IsIncreasing(t, locked.CampaignIDs)
		require.ElementsMatch(t, []string{scheduledRun, sendingRun}, locked.DeliveryRunIDs)
		require.ElementsMatch(t, []string{first, second}, drafted)
		require.IsIncreasing(t, drafted)
		require.Equal(t, Campaign{SegmentID: segment.ID, Status: CampaignDraft}, h.CampaignOf(t, first))
		require.Equal(t, Campaign{SegmentID: segment.ID, Status: CampaignDraft}, h.CampaignOf(t, draft))
		require.Equal(t, DeliveryRun{SegmentID: segment.ID, Status: RunCancelled, Completed: true}, h.DeliveryRunOf(t, scheduledRun))
		require.Equal(t, DeliveryRun{SegmentID: segment.ID, Status: RunSending}, h.DeliveryRunOf(t, sendingRun))
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			changed, err := store.CampaignUsageChanged(ctx, segment.ID, locked)
			require.True(t, changed, "the cancelled run no longer uses the segment")
			return err
		}))
	},
	"download policies lock attachments, then Tracks, with every segment they grant through": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Granting", audience.SegmentMembersByFilter, audience.Config{})
		other := insert(t, h, "Also granting", audience.SegmentMembersByFilter, audience.Config{})
		fresh := insert(t, h, "Granting nothing", audience.SegmentMembersByFilter, audience.Config{})
		shared := h.AttachmentPolicy(t, segment.ID, other.ID)
		single := h.AttachmentPolicy(t, segment.ID)
		track := h.TrackPolicy(t, segment.ID, other.ID)
		h.AttachmentPolicy(t, other.ID)
		attachments := []audience.DownloadPolicy{shared, single}
		if attachments[0].ItemID > attachments[1].ItemID {
			attachments[0], attachments[1] = attachments[1], attachments[0]
		}

		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			policies, err := store.LockDownloadPolicies(ctx, segment.ID)
			if err != nil {
				return err
			}
			require.Equal(t, append(attachments, track), policies)
			granting, err := store.GrantsDownloadAccess(ctx, segment.ID)
			require.True(t, granting)
			if err != nil {
				return err
			}
			granting, err = store.GrantsDownloadAccess(ctx, fresh.ID)
			require.False(t, granting)
			if err != nil {
				return err
			}
			return store.DetachDownloadPolicies(ctx, segment.ID)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			granting, err := store.GrantsDownloadAccess(ctx, segment.ID)
			require.False(t, granting)
			if err != nil {
				return err
			}
			policies, err := store.LockDownloadPolicies(ctx, other.ID)
			require.Len(t, policies, 3)
			for _, policy := range policies {
				require.Equal(t, []string{other.ID}, policy.SegmentIDs)
			}
			return err
		}))
	},
	"eligible Members are the onboarded ones with an Identity link": func(t *testing.T, h Harness) {
		eligible, ineligible := h.Member(t, true), h.Member(t, false)
		require.NoError(t, transact(t, h, func(ctx context.Context, store audience.Store) error {
			members, err := store.EligibleMemberIDs(ctx, []string{eligible, ineligible})
			require.Equal(t, []string{eligible}, members)
			return err
		}))
	},
	"a failed transaction leaves no segment behind": func(t *testing.T, h Harness) {
		failed := errors.New("later step failed")
		var created audience.Segment
		err := transact(t, h, func(ctx context.Context, store audience.Store) (err error) {
			if created, err = store.InsertSegment(ctx, audience.Segment{Name: "Rolled back", Type: audience.SegmentAllMembers}); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store audience.Store) error {
			_, err := store.Segment(ctx, created.ID)
			return err
		}), audience.ErrSegmentNotFound)
	},
	"a locked segment makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Contended", audience.SegmentAllMembers, audience.Config{})
		name := "Renamed by the first writer"
		var seen audience.Segment
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store audience.Store) error {
				_, err := store.LockSegment(ctx, segment.ID)
				return err
			},
			func(ctx context.Context, store audience.Store) error {
				return store.UpdateSegment(ctx, segment.ID, audience.SegmentChange{Name: &name, UpdatedAt: time.Now().UTC()})
			},
			func(ctx context.Context, store audience.Store) (err error) {
				seen, err = store.LockSegment(ctx, segment.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, name, seen.Name)
	},
	"locked campaign usage makes a second archive wait for the first commit": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Contended usage", audience.SegmentMembersByFilter, audience.Config{})
		campaign := h.Campaign(t, segment.ID, CampaignScheduled)
		var seen audience.CampaignUsage
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store audience.Store) error {
				_, err := store.LockCampaignUsage(ctx, segment.ID)
				return err
			},
			func(ctx context.Context, store audience.Store) error {
				_, err := store.ReleaseScheduledCampaigns(ctx, segment.ID, time.Now().UTC())
				return err
			},
			func(ctx context.Context, store audience.Store) (err error) {
				seen, err = store.LockCampaignUsage(ctx, segment.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, []string{campaign}, seen.CampaignIDs)
		require.Equal(t, CampaignDraft, h.CampaignOf(t, campaign).Status)
	},
	"locked download policies make a second archive wait for the first commit": func(t *testing.T, h Harness) {
		segment := insert(t, h, "Contended policies", audience.SegmentMembersByFilter, audience.Config{})
		h.AttachmentPolicy(t, segment.ID)
		h.TrackPolicy(t, segment.ID)
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store audience.Store) error {
				_, err := store.LockDownloadPolicies(ctx, segment.ID)
				return err
			},
			func(ctx context.Context, store audience.Store) error {
				return store.DetachDownloadPolicies(ctx, segment.ID)
			},
			func(ctx context.Context, store audience.Store) error {
				_, err := store.LockDownloadPolicies(ctx, segment.ID)
				return err
			},
		)
		require.NoError(t, err)
	},
}
