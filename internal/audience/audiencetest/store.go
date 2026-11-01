// Package audiencetest provides the in-memory audience.Store unit tests use
// in place of audience/postgres, and the Store contract both keep.
package audiencetest

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/audience"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// The campaign and delivery run statuses the Store distinguishes.
const (
	CampaignDraft     = "draft"
	CampaignScheduled = "scheduled"
	CampaignSending   = "sending"

	RunScheduled = "scheduled"
	RunSending   = "sending"
	RunCancelled = "cancelled"
)

// Store implements audience.Store in memory. It knows no schema and takes
// no lock; its unit of work serializes transactions. Like PostgreSQL it
// stores times at microsecond precision.
type Store struct {
	mu        sync.Mutex
	segments  map[string]audience.Segment
	tags      map[string]bool
	members   map[string]bool // eligible
	campaigns map[string]Campaign
	runs      map[string]DeliveryRun
	policies  []audience.DownloadPolicy
	locks     []string

	// BeforeUsageCheck, when set, runs at the start of every
	// CampaignUsageChanged, so a test can change what targets a segment while
	// an archive takes its locks, as a concurrent scheduler would. What it
	// changes rolls back with the transaction.
	BeforeUsageCheck func(store *Store)
}

// Campaign is a campaign that targets a segment.
type Campaign struct {
	SegmentID string
	Status    string
	Scheduled bool
}

// DeliveryRun is a delivery run that targets a segment.
type DeliveryRun struct {
	SegmentID string
	Status    string
	Completed bool
}

// New returns an empty Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[audience.Store]) {
	store := &Store{
		segments:  map[string]audience.Segment{},
		tags:      map[string]bool{},
		members:   map[string]bool{},
		campaigns: map[string]Campaign{},
		runs:      map[string]DeliveryRun{},
	}
	return store, mutationtest.New[audience.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	segments := make(map[string]audience.Segment, len(s.segments))
	for id, segment := range s.segments {
		segments[id] = cloneSegment(segment)
	}
	tags, members := maps.Clone(s.tags), maps.Clone(s.members)
	campaigns, runs := maps.Clone(s.campaigns), maps.Clone(s.runs)
	policies := clonePolicies(s.policies)
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.segments, s.tags, s.members = segments, tags, members
		s.campaigns, s.runs, s.policies = campaigns, runs, policies
	}
}

// SeedSegment stores segment as if it had been created, keeping its ID and
// timestamps; a blank ID gets a new one and a zero CreatedAt now. It returns
// the stored segment.
func (s *Store) SeedSegment(segment audience.Segment) audience.Segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if segment.ID == "" {
		segment.ID = uuid.NewString()
	}
	if segment.CreatedAt.IsZero() {
		segment.CreatedAt = now()
		segment.UpdatedAt = &segment.CreatedAt
	}
	segment = storedSegment(segment)
	s.segments[segment.ID] = segment
	return cloneSegment(segment)
}

// Stored returns the stored segment.
func (s *Store) Stored(id string) (audience.Segment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment, ok := s.segments[id]
	return cloneSegment(segment), ok
}

// SegmentCount counts the stored segments.
func (s *Store) SegmentCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.segments)
}

// SeedMemberTag stores a Member tag and returns its ID.
func (s *Store) SeedMemberTag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.tags[id] = true
	return id
}

// SeedMember stores a Member, onboarded with an exact Identity link when
// eligible, and returns its ID.
func (s *Store) SeedMember(eligible bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.members[id] = eligible
	return id
}

// SeedCampaign stores a campaign of the segment and returns its ID; a
// scheduled campaign has a scheduled time.
func (s *Store) SeedCampaign(segmentID, status string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.campaigns[id] = Campaign{SegmentID: segmentID, Status: status, Scheduled: status == CampaignScheduled}
	return id
}

// SeedDeliveryRun stores a delivery run of the segment and returns its ID.
func (s *Store) SeedDeliveryRun(segmentID, status string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.runs[id] = DeliveryRun{SegmentID: segmentID, Status: status}
	return id
}

// SeedDownloadPolicy stores a download policy; its SegmentIDs are sorted.
func (s *Store) SeedDownloadPolicy(policy audience.DownloadPolicy) audience.DownloadPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy.SegmentIDs = slices.Sorted(slices.Values(policy.SegmentIDs))
	s.policies = append(s.policies, clonePolicies([]audience.DownloadPolicy{policy})[0])
	return policy
}

// CampaignOf returns a stored campaign.
func (s *Store) CampaignOf(id string) Campaign {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.campaigns[id]
}

// DeliveryRunOf returns a stored delivery run.
func (s *Store) DeliveryRunOf(id string) DeliveryRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

// PolicySegmentIDs returns the segments the policy of itemID grants
// through.
func (s *Store) PolicySegmentIDs(itemID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, policy := range s.policies {
		if policy.ItemID == itemID {
			return slices.Clone(policy.SegmentIDs)
		}
	}
	return nil
}

// Locks returns the lock methods called so far, in order: "campaign usage",
// "segment" and "download policies".
func (s *Store) Locks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.locks)
}

func (s *Store) Segment(_ context.Context, id string) (audience.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment, ok := s.segments[id]
	if !ok {
		return audience.Segment{}, audience.ErrSegmentNotFound
	}
	return cloneSegment(segment), nil
}

func (s *Store) SegmentArchived(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment, ok := s.segments[id]
	if !ok {
		return false, audience.ErrSegmentNotFound
	}
	return segment.ArchivedAt != nil, nil
}

func (s *Store) LockSegment(ctx context.Context, id string) (audience.Segment, error) {
	s.lock("segment")
	return s.Segment(ctx, id)
}

func (s *Store) ListSegments(_ context.Context, includeArchived bool, page audience.Page) ([]audience.Segment, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(page, func(segment audience.Segment) bool {
		return includeArchived || segment.ArchivedAt == nil
	}, true)
}

func (s *Store) ListAuthenticatedAccessSegments(_ context.Context, page audience.Page) ([]audience.Segment, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(page, func(segment audience.Segment) bool {
		return segment.ArchivedAt == nil && (segment.Type == audience.SegmentMemberTags || segment.Type == audience.SegmentMembersByFilter)
	}, false)
}

// list applies the search filter and the sorts the PostgreSQL store allows,
// with the same errors.
func (s *Store) list(page audience.Page, include func(audience.Segment) bool, withConfig bool) ([]audience.Segment, int64, error) {
	var search []string
	for _, filter := range page.Filters {
		if filter == nil {
			continue
		}
		if filter.GetField() != "search" {
			return nil, 0, errs.InvalidFilterField(filter.GetField())
		}
		if filter.GetOp() != commonv1.FilterOp_FILTER_OP_ILIKE {
			return nil, 0, errs.InvalidFilterOp(filter.GetField(), filter.GetOp().String())
		}
		if filter.GetValue() != "" {
			search = append(search, strings.ToLower(filter.GetValue()))
		}
	}
	var selected []audience.Segment
	for _, segment := range s.segments {
		name := strings.ToLower(segment.Name)
		if include(segment) && !slices.ContainsFunc(search, func(value string) bool { return !strings.Contains(name, value) }) {
			selected = append(selected, cloneSegment(segment))
		}
	}
	compare, err := segmentOrder(page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	slices.SortStableFunc(selected, compare)
	total := int64(len(selected))
	start := min(int(page.Offset), len(selected))
	end := min(start+int(page.Limit), len(selected))
	selected = selected[start:end]
	if !withConfig {
		for i := range selected {
			selected[i].Config = audience.Config{
				CreatedAfter: selected[i].Config.CreatedAfter, CreatedBefore: selected[i].Config.CreatedBefore,
			}
		}
	}
	return selected, total, nil
}

func segmentOrder(sorts []*commonv1.SortSpec) (func(a, b audience.Segment) int, error) {
	if len(sorts) == 0 {
		return func(a, b audience.Segment) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
		}, nil
	}
	var compares []func(a, b audience.Segment) int
	for _, sort := range sorts {
		var compare func(a, b audience.Segment) int
		switch sort.GetField() {
		case "name":
			compare = func(a, b audience.Segment) int { return cmp.Compare(a.Name, b.Name) }
		case "created_at":
			compare = func(a, b audience.Segment) int { return a.CreatedAt.Compare(b.CreatedAt) }
		case "segment_type":
			compare = func(a, b audience.Segment) int { return cmp.Compare(a.Type, b.Type) }
		default:
			return nil, errs.InvalidSortField(sort.GetField())
		}
		if sort.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC {
			ascending := compare
			compare = func(a, b audience.Segment) int { return -ascending(a, b) }
		}
		compares = append(compares, compare)
	}
	return func(a, b audience.Segment) int {
		for _, compare := range compares {
			if order := compare(a, b); order != 0 {
				return order
			}
		}
		return 0
	}, nil
}

func (s *Store) SegmentReferences(_ context.Context, ids []string) (map[string]audience.References, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	references := map[string]audience.References{}
	for _, id := range ids {
		var counted audience.References
		for _, campaign := range s.campaigns {
			if campaign.SegmentID == id {
				counted.Campaigns++
			}
		}
		for _, run := range s.runs {
			if run.SegmentID == id {
				counted.DeliveryRuns++
			}
		}
		for _, policy := range s.policies {
			if slices.Contains(policy.SegmentIDs, id) {
				counted.DownloadPolicies++
			}
		}
		if counted != (audience.References{}) {
			references[id] = counted
		}
	}
	return references, nil
}

func (s *Store) InsertSegment(_ context.Context, segment audience.Segment) (audience.Segment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment.ID = uuid.NewString()
	segment.CreatedAt = now()
	segment.UpdatedAt = &segment.CreatedAt
	segment.ArchivedAt = nil
	stored := storedSegment(segment)
	stored.Config = audience.Config{CreatedAfter: stored.Config.CreatedAfter, CreatedBefore: stored.Config.CreatedBefore}
	s.segments[segment.ID] = stored
	created := cloneSegment(stored)
	created.Config = cloneConfig(segment.Config)
	return created, nil
}

func (s *Store) UpdateSegment(_ context.Context, id string, change audience.SegmentChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment, ok := s.segments[id]
	if !ok {
		return nil
	}
	if change.Name != nil {
		segment.Name = *change.Name
	}
	if change.Description != nil {
		description := *change.Description
		segment.Description = &description
	}
	if change.Type != nil {
		segment.Type = *change.Type
	}
	if change.SetCreatedAfter {
		segment.Config.CreatedAfter = change.CreatedAfter
	}
	if change.SetCreatedBefore {
		segment.Config.CreatedBefore = change.CreatedBefore
	}
	updatedAt := change.UpdatedAt
	segment.UpdatedAt = &updatedAt
	s.segments[id] = storedSegment(segment)
	return nil
}

func (s *Store) ReplaceConfig(_ context.Context, id string, config audience.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tagID := range config.MemberTagIDs {
		if !s.tags[tagID] {
			return audience.ErrUnknownReference
		}
	}
	for _, memberID := range config.ExcludeMemberIDs {
		if _, exists := s.members[memberID]; !exists {
			return audience.ErrUnknownReference
		}
	}
	segment, ok := s.segments[id]
	if !ok {
		return nil
	}
	segment.Config.MemberTagIDs = sortedOrNil(config.MemberTagIDs)
	segment.Config.AccountRoles = sortedOrNil(config.AccountRoles)
	segment.Config.ExcludeMemberIDs = sortedOrNil(config.ExcludeMemberIDs)
	s.segments[id] = segment
	return nil
}

func (s *Store) ArchiveSegment(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment, ok := s.segments[id]
	if !ok || segment.ArchivedAt != nil {
		return audience.ErrArchiveStateChanged
	}
	at = at.Truncate(time.Microsecond)
	segment.ArchivedAt, segment.UpdatedAt = &at, &at
	s.segments[id] = segment
	return nil
}

func (s *Store) RestoreSegment(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment, ok := s.segments[id]
	if !ok || segment.ArchivedAt == nil {
		return audience.ErrArchiveStateChanged
	}
	at = at.Truncate(time.Microsecond)
	segment.ArchivedAt, segment.UpdatedAt = nil, &at
	s.segments[id] = segment
	return nil
}

func (s *Store) EligibleMemberIDs(_ context.Context, memberIDs []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	eligible := []string{}
	for _, memberID := range memberIDs {
		if s.members[memberID] {
			eligible = append(eligible, memberID)
		}
	}
	return eligible, nil
}

func (s *Store) SegmentInActiveDelivery(_ context.Context, id string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, campaign := range s.campaigns {
		if campaign.SegmentID == id && (campaign.Status == CampaignScheduled || campaign.Status == CampaignSending) {
			return true, false, nil
		}
	}
	for _, run := range s.runs {
		if run.SegmentID == id && (run.Status == RunScheduled || run.Status == RunSending) {
			return false, true, nil
		}
	}
	return false, false, nil
}

func (s *Store) LockCampaignUsage(_ context.Context, id string) (audience.CampaignUsage, error) {
	s.lock("campaign usage")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage(id), nil
}

func (s *Store) CampaignUsageChanged(_ context.Context, id string, locked audience.CampaignUsage) (bool, error) {
	if s.BeforeUsageCheck != nil {
		s.BeforeUsageCheck(s)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.usage(id)
	return !slices.Equal(current.CampaignIDs, locked.CampaignIDs) || !slices.Equal(current.DeliveryRunIDs, locked.DeliveryRunIDs), nil
}

func (s *Store) usage(id string) audience.CampaignUsage {
	usage := audience.CampaignUsage{CampaignIDs: []string{}, DeliveryRunIDs: []string{}}
	for campaignID, campaign := range s.campaigns {
		if campaign.SegmentID == id {
			usage.CampaignIDs = append(usage.CampaignIDs, campaignID)
		}
	}
	for runID, run := range s.runs {
		if run.SegmentID == id && (run.Status == RunScheduled || run.Status == RunSending) {
			usage.DeliveryRunIDs = append(usage.DeliveryRunIDs, runID)
		}
	}
	slices.Sort(usage.CampaignIDs)
	slices.Sort(usage.DeliveryRunIDs)
	return usage
}

func (s *Store) ReleaseScheduledCampaigns(_ context.Context, id string, _ time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	drafted := []string{}
	for runID, run := range s.runs {
		if run.SegmentID == id && run.Status == RunScheduled {
			run.Status, run.Completed = RunCancelled, true
			s.runs[runID] = run
		}
	}
	for campaignID, campaign := range s.campaigns {
		if campaign.SegmentID == id && campaign.Status == CampaignScheduled {
			campaign.Status, campaign.Scheduled = CampaignDraft, false
			s.campaigns[campaignID] = campaign
			drafted = append(drafted, campaignID)
		}
	}
	slices.Sort(drafted)
	return drafted, nil
}

func (s *Store) GrantsDownloadAccess(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, policy := range s.policies {
		if slices.Contains(policy.SegmentIDs, id) {
			return true, nil
		}
	}
	return false, nil
}

// LockDownloadPolicies returns the File attachment policies by item, then
// the Track policies by item, as the PostgreSQL store orders them.
func (s *Store) LockDownloadPolicies(_ context.Context, id string) ([]audience.DownloadPolicy, error) {
	s.lock("download policies")
	s.mu.Lock()
	defer s.mu.Unlock()
	var policies []audience.DownloadPolicy
	for _, policy := range s.policies {
		if slices.Contains(policy.SegmentIDs, id) {
			policies = append(policies, policy)
		}
	}
	policies = clonePolicies(policies)
	slices.SortFunc(policies, func(a, b audience.DownloadPolicy) int {
		return cmp.Or(cmp.Compare(boolOrder(a.Owner == "release"), boolOrder(b.Owner == "release")), cmp.Compare(a.ItemID, b.ItemID))
	})
	return policies, nil
}

func (s *Store) DetachDownloadPolicies(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.policies {
		s.policies[i].SegmentIDs = slices.DeleteFunc(slices.Clone(s.policies[i].SegmentIDs), func(segmentID string) bool { return segmentID == id })
	}
	return nil
}

func (s *Store) lock(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locks = append(s.locks, name)
}

func boolOrder(value bool) int {
	if value {
		return 1
	}
	return 0
}

func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// storedSegment is segment as PostgreSQL keeps it: times at microsecond
// precision.
func storedSegment(segment audience.Segment) audience.Segment {
	segment = cloneSegment(segment)
	segment.Config.CreatedAfter = truncated(segment.Config.CreatedAfter)
	segment.Config.CreatedBefore = truncated(segment.Config.CreatedBefore)
	segment.ArchivedAt = truncated(segment.ArchivedAt)
	segment.UpdatedAt = truncated(segment.UpdatedAt)
	segment.CreatedAt = segment.CreatedAt.Truncate(time.Microsecond)
	return segment
}

func truncated(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	stored := value.Truncate(time.Microsecond)
	return &stored
}

func sortedOrNil(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return slices.Sorted(slices.Values(values))
}

func cloneSegment(segment audience.Segment) audience.Segment {
	if segment.Description != nil {
		description := *segment.Description
		segment.Description = &description
	}
	segment.Config = cloneConfig(segment.Config)
	segment.ArchivedAt = clonedTime(segment.ArchivedAt)
	segment.UpdatedAt = clonedTime(segment.UpdatedAt)
	return segment
}

func clonedTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneConfig(config audience.Config) audience.Config {
	config.MemberTagIDs = slices.Clone(config.MemberTagIDs)
	config.AccountRoles = slices.Clone(config.AccountRoles)
	config.ExcludeMemberIDs = slices.Clone(config.ExcludeMemberIDs)
	config.CreatedAfter = clonedTime(config.CreatedAfter)
	config.CreatedBefore = clonedTime(config.CreatedBefore)
	return config
}

func clonePolicies(policies []audience.DownloadPolicy) []audience.DownloadPolicy {
	cloned := slices.Clone(policies)
	for i := range cloned {
		cloned[i].SegmentIDs = slices.Clone(cloned[i].SegmentIDs)
	}
	return cloned
}
