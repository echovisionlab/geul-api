package criteriatest

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
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
	Campaign(t *testing.T, criteriaID, status string) string
	DeliveryRun(t *testing.T, campaignID, criteriaID, status string) string
	// AttachmentPolicy stores a Post's restricted File attachment that grants
	// through criteriaIDs; TrackPolicy a Release's restricted Track.
	AttachmentPolicy(t *testing.T, criteriaIDs ...string) membercriteria.DownloadPolicy
	TrackPolicy(t *testing.T, criteriaIDs ...string) membercriteria.DownloadPolicy
	CampaignOf(t *testing.T, id string) Campaign
	DeliveryRunOf(t *testing.T, id string) DeliveryRun
}

// Harness is one Store implementation and its fixtures.
type Harness struct {
	mutationtest.Harness[membercriteria.Store]
	Fixtures
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store, _ := New()
	return Harness{Harness: mutationtest.FakeHarness[membercriteria.Store](store), Fixtures: fakeFixtures{store}}
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
func (f fakeFixtures) Campaign(_ *testing.T, criteriaID, status string) string {
	return f.store.SeedCampaign(criteriaID, status)
}
func (f fakeFixtures) DeliveryRun(_ *testing.T, _, criteriaID, status string) string {
	return f.store.SeedDeliveryRun(criteriaID, status)
}
func (f fakeFixtures) AttachmentPolicy(_ *testing.T, criteriaIDs ...string) membercriteria.DownloadPolicy {
	return f.store.SeedDownloadPolicy(membercriteria.DownloadPolicy{
		Owner: "post", OwnerID: uuid.NewString(), ItemID: uuid.NewString(), FileID: uuid.NewString(),
		Criteria: "restricted", CriteriaIDs: criteriaIDs,
	})
}
func (f fakeFixtures) TrackPolicy(_ *testing.T, criteriaIDs ...string) membercriteria.DownloadPolicy {
	return f.store.SeedDownloadPolicy(membercriteria.DownloadPolicy{
		Owner: "release", OwnerID: uuid.NewString(), ItemID: uuid.NewString(), FileID: uuid.NewString(),
		Criteria: "restricted", CriteriaIDs: criteriaIDs,
	})
}
func (f fakeFixtures) CampaignOf(_ *testing.T, id string) Campaign { return f.store.CampaignOf(id) }
func (f fakeFixtures) DeliveryRunOf(_ *testing.T, id string) DeliveryRun {
	return f.store.DeliveryRunOf(id)
}

// insert stores a criteria of kind with config through the Store.
func insert(t *testing.T, h Harness, name string, kind membercriteria.Kind, config membercriteria.Config) membercriteria.Criteria {
	t.Helper()
	var created membercriteria.Criteria
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, store membercriteria.Store) (err error) {
		created, err = store.InsertCriteria(ctx, membercriteria.Criteria{Name: name, Type: kind, Config: config})
		if err != nil {
			return err
		}
		return store.ReplaceConfig(ctx, created.ID, config)
	}))
	return created
}

func read(t *testing.T, h Harness, id string) membercriteria.Criteria {
	t.Helper()
	var criteria membercriteria.Criteria
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) (err error) {
		criteria, err = store.Criteria(ctx, id)
		return err
	}))
	return criteria
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store membercriteria.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func list(t *testing.T, h Harness, includeArchived bool, page membercriteria.Page) ([]string, int64) {
	t.Helper()
	var criteriaSet []membercriteria.Criteria
	var total int64
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) (err error) {
		criteriaSet, total, err = store.ListCriteriaSet(ctx, includeArchived, page)
		return err
	}))
	names := make([]string, len(criteriaSet))
	for i, criteria := range criteriaSet {
		names[i] = criteria.Name
	}
	return names, total
}

// storeContract is the behavior every membercriteria.Store has.
var storeContract = map[string]func(t *testing.T, h Harness){
	"an inserted criteria reads back with its ID, timestamps and filter at microsecond precision": func(t *testing.T, h Harness) {
		tags := []string{h.MemberTag(t), h.MemberTag(t)}
		member := h.Member(t, true)
		after := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.UTC)
		description := "Described"
		config := membercriteria.Config{
			MemberTagIDs: tags, AccountRoles: []string{policyv1.Role.Admin().ID(), policyv1.Role.User().ID()},
			CreatedAfter: &after, ExcludeMemberIDs: []string{member},
		}
		var created membercriteria.Criteria
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) (err error) {
			created, err = store.InsertCriteria(ctx, membercriteria.Criteria{
				Name: "Inserted", Description: &description, Type: membercriteria.KindFilter, Config: config,
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
		require.Equal(t, after, *created.Config.CreatedAfter, "the inserted criteria carries the filter as given")

		stored := read(t, h, created.ID)
		require.Equal(t, "Inserted", stored.Name)
		require.Equal(t, description, *stored.Description)
		require.Equal(t, membercriteria.KindFilter, stored.Type)
		require.ElementsMatch(t, tags, stored.Config.MemberTagIDs)
		require.IsIncreasing(t, stored.Config.MemberTagIDs)
		require.Equal(t, config.AccountRoles, stored.Config.AccountRoles)
		require.Equal(t, []string{member}, stored.Config.ExcludeMemberIDs)
		require.True(t, after.Truncate(time.Microsecond).Equal(*stored.Config.CreatedAfter))
		require.Nil(t, stored.Config.CreatedBefore)
		require.True(t, created.CreatedAt.Equal(stored.CreatedAt))

		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			locked, err := store.LockCriteria(ctx, created.ID)
			require.Equal(t, stored.Config, locked.Config)
			if err != nil {
				return err
			}
			archived, err := store.CriteriaArchived(ctx, created.ID)
			require.False(t, archived)
			return err
		}))
	},
	"a missing criteria is not found": func(t *testing.T, h Harness) {
		id := uuid.NewString()
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) error {
			_, err := store.Criteria(ctx, id)
			return err
		}), membercriteria.ErrCriteriaNotFound)
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			_, err := store.LockCriteria(ctx, id)
			return err
		}), membercriteria.ErrCriteriaNotFound)
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			_, err := store.CriteriaArchived(ctx, id)
			return err
		}), membercriteria.ErrCriteriaNotFound)
	},
	"a filter naming a missing Member tag or Member is an unknown reference": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Unknown references", membercriteria.KindFilter, membercriteria.Config{})
		for name, config := range map[string]membercriteria.Config{
			"tag":    {MemberTagIDs: []string{uuid.NewString()}},
			"member": {ExcludeMemberIDs: []string{uuid.NewString()}},
		} {
			err := transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
				return store.ReplaceConfig(ctx, criteria.ID, config)
			})
			require.ErrorIs(t, err, membercriteria.ErrUnknownReference, name)
		}
	},
	"replacing a filter drops the relations it no longer names": func(t *testing.T, h Harness) {
		kept, dropped := h.MemberTag(t), h.MemberTag(t)
		member := h.Member(t, true)
		criteria := insert(t, h, "Replaced", membercriteria.KindFilter, membercriteria.Config{
			MemberTagIDs: []string{kept, dropped}, AccountRoles: []string{policyv1.Role.Author().ID()}, ExcludeMemberIDs: []string{member},
		})
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.ReplaceConfig(ctx, criteria.ID, membercriteria.Config{MemberTagIDs: []string{kept}})
		}))
		stored := read(t, h, criteria.ID)
		require.Equal(t, []string{kept}, stored.Config.MemberTagIDs)
		require.Empty(t, stored.Config.AccountRoles)
		require.Empty(t, stored.Config.ExcludeMemberIDs)
	},
	"an update changes only the columns it names": func(t *testing.T, h Harness) {
		after := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
		criteria := insert(t, h, "Before update", membercriteria.KindFilter, membercriteria.Config{CreatedAfter: &after})
		name := "After update"
		updatedAt := time.Date(2026, time.April, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.UpdateCriteria(ctx, criteria.ID, membercriteria.CriteriaChange{Name: &name, SetCreatedAfter: true, UpdatedAt: updatedAt})
		}))
		stored := read(t, h, criteria.ID)
		require.Equal(t, name, stored.Name)
		require.Nil(t, stored.Description)
		require.Nil(t, stored.Config.CreatedAfter, "a set time without a value clears it")
		require.Equal(t, membercriteria.KindFilter, stored.Type)
		require.True(t, updatedAt.Equal(*stored.UpdatedAt))

		description, kind := "Described", membercriteria.KindAllMembers
		before := time.Date(2026, time.May, 1, 2, 3, 4, 567891234, time.UTC)
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.UpdateCriteria(ctx, criteria.ID, membercriteria.CriteriaChange{
				Description: &description, Type: &kind, SetCreatedBefore: true, CreatedBefore: &before, UpdatedAt: updatedAt,
			})
		}))
		stored = read(t, h, criteria.ID)
		require.Equal(t, name, stored.Name)
		require.Equal(t, description, *stored.Description)
		require.Equal(t, kind, stored.Type)
		require.True(t, before.Truncate(time.Microsecond).Equal(*stored.Config.CreatedBefore))
	},
	"archive and restore apply only from the opposite state": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Lifecycle", membercriteria.KindFilter, membercriteria.Config{})
		archivedAt := time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.ArchiveCriteria(ctx, criteria.ID, archivedAt)
		}))
		stored := read(t, h, criteria.ID)
		require.True(t, archivedAt.Equal(*stored.ArchivedAt))
		require.True(t, archivedAt.Equal(*stored.UpdatedAt))
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.ArchiveCriteria(ctx, criteria.ID, archivedAt)
		}), membercriteria.ErrArchiveStateChanged)
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			archived, err := store.CriteriaArchived(ctx, criteria.ID)
			require.True(t, archived)
			return err
		}))

		restoredAt := archivedAt.Add(time.Hour)
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.RestoreCriteria(ctx, criteria.ID, restoredAt)
		}))
		stored = read(t, h, criteria.ID)
		require.Nil(t, stored.ArchivedAt)
		require.True(t, restoredAt.Equal(*stored.UpdatedAt))
		require.ErrorIs(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.RestoreCriteria(ctx, criteria.ID, restoredAt)
		}), membercriteria.ErrArchiveStateChanged)
	},
	"a list holds active Member criteria unless asked, in name and ID order, with the total": func(t *testing.T, h Harness) {
		tag := h.MemberTag(t)
		insert(t, h, "Beta", membercriteria.KindMemberTags, membercriteria.Config{MemberTagIDs: []string{tag}})
		insert(t, h, "Alpha", membercriteria.KindAllMembers, membercriteria.Config{})
		gamma := insert(t, h, "Gamma", membercriteria.KindFilter, membercriteria.Config{})
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.ArchiveCriteria(ctx, gamma.ID, time.Now().UTC())
		}))

		names, total := list(t, h, false, membercriteria.Page{Limit: 20})
		require.Equal(t, []string{"Alpha", "Beta"}, names)
		require.EqualValues(t, 2, total)
		names, total = list(t, h, true, membercriteria.Page{Limit: 20})
		require.Equal(t, []string{"Alpha", "Beta", "Gamma"}, names)
		require.EqualValues(t, 3, total)
		names, total = list(t, h, true, membercriteria.Page{Limit: 1, Offset: 1})
		require.Equal(t, []string{"Beta"}, names)
		require.EqualValues(t, 3, total)
		names, total = list(t, h, true, membercriteria.Page{Limit: 20, Sorts: []*commonv1.SortSpec{{Field: "name", Order: commonv1.SortOrder_SORT_ORDER_DESC}}})
		require.Equal(t, []string{"Gamma", "Beta", "Alpha"}, names)
		require.EqualValues(t, 3, total)
		names, total = list(t, h, true, membercriteria.Page{Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "ALP"}}})
		require.Equal(t, []string{"Alpha"}, names)
		require.EqualValues(t, 1, total)

		var criteriaSet []membercriteria.Criteria
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) (err error) {
			criteriaSet, _, err = store.ListCriteriaSet(ctx, false, membercriteria.Page{Limit: 20})
			return err
		}))
		require.Equal(t, []string{tag}, criteriaSet[1].Config.MemberTagIDs, "a listed criteria carries its filter")

		for name, page := range map[string]membercriteria.Page{
			"filter field": {Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "name", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "a"}}},
			"filter op":    {Limit: 20, Filters: []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "a"}}},
			"sort field":   {Limit: 20, Sorts: []*commonv1.SortSpec{{Field: "id"}}},
		} {
			err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) error {
				_, _, err := store.ListCriteriaSet(ctx, true, page)
				return err
			})
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code(), name)
		}
	},
	"the authenticated access list holds active member-tag and member-filter Member criteria without filters": func(t *testing.T, h Harness) {
		tag := h.MemberTag(t)
		insert(t, h, "All members", membercriteria.KindAllMembers, membercriteria.Config{})
		insert(t, h, "Tagged", membercriteria.KindMemberTags, membercriteria.Config{MemberTagIDs: []string{tag}})
		insert(t, h, "Filtered", membercriteria.KindFilter, membercriteria.Config{AccountRoles: []string{policyv1.Role.User().ID()}})
		archived := insert(t, h, "Archived filter", membercriteria.KindFilter, membercriteria.Config{})
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			return store.ArchiveCriteria(ctx, archived.ID, time.Now().UTC())
		}))
		var criteriaSet []membercriteria.Criteria
		var total int64
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) (err error) {
			criteriaSet, total, err = store.ListAuthenticatedAccessCriteriaSet(ctx, membercriteria.Page{Limit: 20})
			return err
		}))
		require.EqualValues(t, 2, total)
		require.Len(t, criteriaSet, 2)
		require.Equal(t, "Filtered", criteriaSet[0].Name)
		require.Equal(t, "Tagged", criteriaSet[1].Name)
		for _, criteria := range criteriaSet {
			require.Empty(t, criteria.Config.MemberTagIDs)
			require.Empty(t, criteria.Config.AccountRoles)
		}
	},
	"references count campaigns, delivery runs and download policies": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Referenced", membercriteria.KindFilter, membercriteria.Config{})
		other := insert(t, h, "Other", membercriteria.KindFilter, membercriteria.Config{})
		unused := insert(t, h, "Unused", membercriteria.KindFilter, membercriteria.Config{})
		h.Campaign(t, criteria.ID, CampaignDraft)
		scheduled := h.Campaign(t, criteria.ID, CampaignScheduled)
		h.DeliveryRun(t, scheduled, criteria.ID, RunScheduled)
		h.AttachmentPolicy(t, criteria.ID)
		h.TrackPolicy(t, criteria.ID, other.ID)
		var references map[string]membercriteria.References
		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) (err error) {
			references, err = store.CriteriaReferences(ctx, []string{criteria.ID, other.ID, unused.ID})
			return err
		}))
		require.Equal(t, map[string]membercriteria.References{
			criteria.ID: {Campaigns: 2, DeliveryRuns: 1, DownloadPolicies: 2},
			other.ID:    {DownloadPolicies: 1},
		}, references)
	},
	"active delivery is a scheduled or sending campaign or delivery run": func(t *testing.T, h Harness) {
		idle := insert(t, h, "Idle", membercriteria.KindAllMembers, membercriteria.Config{})
		h.Campaign(t, idle.ID, CampaignDraft)
		scheduled := insert(t, h, "Scheduled", membercriteria.KindAllMembers, membercriteria.Config{})
		h.Campaign(t, scheduled.ID, CampaignScheduled)
		running := insert(t, h, "Running", membercriteria.KindAllMembers, membercriteria.Config{})
		h.DeliveryRun(t, h.Campaign(t, running.ID, CampaignDraft), running.ID, RunSending)
		for criteriaID, want := range map[string][2]bool{idle.ID: {false, false}, scheduled.ID: {true, false}, running.ID: {false, true}} {
			require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
				campaign, run, err := store.CriteriaInActiveDelivery(ctx, criteriaID)
				require.Equal(t, want, [2]bool{campaign, run})
				return err
			}))
		}
	},
	"releasing a criteria drafts its scheduled campaigns and cancels only its scheduled runs": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Released", membercriteria.KindFilter, membercriteria.Config{})
		first := h.Campaign(t, criteria.ID, CampaignScheduled)
		second := h.Campaign(t, criteria.ID, CampaignScheduled)
		draft := h.Campaign(t, criteria.ID, CampaignDraft)
		scheduledRun := h.DeliveryRun(t, first, criteria.ID, RunScheduled)
		sendingRun := h.DeliveryRun(t, second, criteria.ID, RunSending)

		var locked membercriteria.CampaignUsage
		var drafted []string
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) (err error) {
			if locked, err = store.LockCampaignUsage(ctx, criteria.ID); err != nil {
				return err
			}
			changed, err := store.CampaignUsageChanged(ctx, criteria.ID, locked)
			require.False(t, changed)
			if err != nil {
				return err
			}
			drafted, err = store.ReleaseScheduledCampaigns(ctx, criteria.ID, time.Now().UTC())
			return err
		}))
		require.ElementsMatch(t, []string{first, second, draft}, locked.CampaignIDs)
		require.IsIncreasing(t, locked.CampaignIDs)
		require.ElementsMatch(t, []string{scheduledRun, sendingRun}, locked.DeliveryRunIDs)
		require.ElementsMatch(t, []string{first, second}, drafted)
		require.IsIncreasing(t, drafted)
		require.Equal(t, Campaign{CriteriaID: criteria.ID, Status: CampaignDraft}, h.CampaignOf(t, first))
		require.Equal(t, Campaign{CriteriaID: criteria.ID, Status: CampaignDraft}, h.CampaignOf(t, draft))
		require.Equal(t, DeliveryRun{CriteriaID: criteria.ID, Status: RunCancelled, Completed: true}, h.DeliveryRunOf(t, scheduledRun))
		require.Equal(t, DeliveryRun{CriteriaID: criteria.ID, Status: RunSending}, h.DeliveryRunOf(t, sendingRun))
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			changed, err := store.CampaignUsageChanged(ctx, criteria.ID, locked)
			require.True(t, changed, "the cancelled run no longer uses the criteria")
			return err
		}))
	},
	"download policies lock attachments, then Tracks, with every criteria they grant through": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Granting", membercriteria.KindFilter, membercriteria.Config{})
		other := insert(t, h, "Also granting", membercriteria.KindFilter, membercriteria.Config{})
		fresh := insert(t, h, "Granting nothing", membercriteria.KindFilter, membercriteria.Config{})
		shared := h.AttachmentPolicy(t, criteria.ID, other.ID)
		single := h.AttachmentPolicy(t, criteria.ID)
		track := h.TrackPolicy(t, criteria.ID, other.ID)
		h.AttachmentPolicy(t, other.ID)
		attachments := []membercriteria.DownloadPolicy{shared, single}
		if attachments[0].ItemID > attachments[1].ItemID {
			attachments[0], attachments[1] = attachments[1], attachments[0]
		}

		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			policies, err := store.LockDownloadPolicies(ctx, criteria.ID)
			if err != nil {
				return err
			}
			require.Equal(t, append(attachments, track), policies)
			granting, err := store.GrantsDownloadAccess(ctx, criteria.ID)
			require.True(t, granting)
			if err != nil {
				return err
			}
			granting, err = store.GrantsDownloadAccess(ctx, fresh.ID)
			require.False(t, granting)
			if err != nil {
				return err
			}
			return store.DetachDownloadPolicies(ctx, criteria.ID)
		}))
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			granting, err := store.GrantsDownloadAccess(ctx, criteria.ID)
			require.False(t, granting)
			if err != nil {
				return err
			}
			policies, err := store.LockDownloadPolicies(ctx, other.ID)
			require.Len(t, policies, 3)
			for _, policy := range policies {
				require.Equal(t, []string{other.ID}, policy.CriteriaIDs)
			}
			return err
		}))
	},
	"eligible Members are the onboarded ones with an Identity link": func(t *testing.T, h Harness) {
		eligible, ineligible := h.Member(t, true), h.Member(t, false)
		require.NoError(t, transact(t, h, func(ctx context.Context, store membercriteria.Store) error {
			members, err := store.EligibleMemberIDs(ctx, []string{eligible, ineligible})
			require.Equal(t, []string{eligible}, members)
			return err
		}))
	},
	"a failed transaction leaves no criteria behind": func(t *testing.T, h Harness) {
		failed := errors.New("later step failed")
		var created membercriteria.Criteria
		err := transact(t, h, func(ctx context.Context, store membercriteria.Store) (err error) {
			if created, err = store.InsertCriteria(ctx, membercriteria.Criteria{Name: "Rolled back", Type: membercriteria.KindAllMembers}); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store membercriteria.Store) error {
			_, err := store.Criteria(ctx, created.ID)
			return err
		}), membercriteria.ErrCriteriaNotFound)
	},
	"a locked criteria makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Contended", membercriteria.KindAllMembers, membercriteria.Config{})
		name := "Renamed by the first writer"
		var seen membercriteria.Criteria
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store membercriteria.Store) error {
				_, err := store.LockCriteria(ctx, criteria.ID)
				return err
			},
			func(ctx context.Context, store membercriteria.Store) error {
				return store.UpdateCriteria(ctx, criteria.ID, membercriteria.CriteriaChange{Name: &name, UpdatedAt: time.Now().UTC()})
			},
			func(ctx context.Context, store membercriteria.Store) (err error) {
				seen, err = store.LockCriteria(ctx, criteria.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, name, seen.Name)
	},
	"locked campaign usage makes a second archive wait for the first commit": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Contended usage", membercriteria.KindFilter, membercriteria.Config{})
		campaign := h.Campaign(t, criteria.ID, CampaignScheduled)
		var seen membercriteria.CampaignUsage
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store membercriteria.Store) error {
				_, err := store.LockCampaignUsage(ctx, criteria.ID)
				return err
			},
			func(ctx context.Context, store membercriteria.Store) error {
				_, err := store.ReleaseScheduledCampaigns(ctx, criteria.ID, time.Now().UTC())
				return err
			},
			func(ctx context.Context, store membercriteria.Store) (err error) {
				seen, err = store.LockCampaignUsage(ctx, criteria.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, []string{campaign}, seen.CampaignIDs)
		require.Equal(t, CampaignDraft, h.CampaignOf(t, campaign).Status)
	},
	"locked download policies make a second archive wait for the first commit": func(t *testing.T, h Harness) {
		criteria := insert(t, h, "Contended policies", membercriteria.KindFilter, membercriteria.Config{})
		h.AttachmentPolicy(t, criteria.ID)
		h.TrackPolicy(t, criteria.ID)
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store membercriteria.Store) error {
				_, err := store.LockDownloadPolicies(ctx, criteria.ID)
				return err
			},
			func(ctx context.Context, store membercriteria.Store) error {
				return store.DetachDownloadPolicies(ctx, criteria.ID)
			},
			func(ctx context.Context, store membercriteria.Store) error {
				_, err := store.LockDownloadPolicies(ctx, criteria.ID)
				return err
			},
		)
		require.NoError(t, err)
	},
}
