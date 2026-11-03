// Package criteriatest provides the in-memory membercriteria.Store unit tests use
// in place of criteria/postgres, and the Store contract both keep.
package criteriatest

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
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

// Store implements membercriteria.Store in memory. It knows no schema and takes
// no lock; its unit of work serializes transactions. Like PostgreSQL it
// stores times at microsecond precision.
type Store struct {
	mu          sync.Mutex
	criteriaSet map[string]membercriteria.Criteria
	tags        map[string]bool
	members     map[string]bool // eligible
	campaigns   map[string]Campaign
	runs        map[string]DeliveryRun
	policies    []membercriteria.DownloadPolicy
	locks       []string

	// BeforeUsageCheck, when set, runs at the start of every
	// CampaignUsageChanged, so a test can change what targets a criteria while
	// an archive takes its locks, as a concurrent scheduler would. What it
	// changes rolls back with the transaction.
	BeforeUsageCheck func(store *Store)
}

// Campaign is a campaign that targets a criteria.
type Campaign struct {
	CriteriaID string
	Status     string
	Scheduled  bool
}

// DeliveryRun is a delivery run that targets a criteria.
type DeliveryRun struct {
	CriteriaID string
	Status     string
	Completed  bool
}

// New returns an empty Store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[membercriteria.Store]) {
	store := &Store{
		criteriaSet: map[string]membercriteria.Criteria{},
		tags:        map[string]bool{},
		members:     map[string]bool{},
		campaigns:   map[string]Campaign{},
		runs:        map[string]DeliveryRun{},
	}
	return store, mutationtest.New[membercriteria.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteriaSet := mutationtest.ClonePersisted(s.criteriaSet)
	tags := mutationtest.ClonePersisted(s.tags)
	members := mutationtest.ClonePersisted(s.members)
	campaigns := mutationtest.ClonePersisted(s.campaigns)
	runs := mutationtest.ClonePersisted(s.runs)
	policies := mutationtest.ClonePersisted(s.policies)
	return mutationtest.Checkpoint{
		State: map[string]any{
			"criteria": criteriaSet,
			"tags":        tags,
			"members":     members,
			"campaigns":   campaigns,
			"runs":        runs,
			"policies":    policies,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.criteriaSet = criteriaSet
			s.tags = tags
			s.members = members
			s.campaigns = campaigns
			s.runs = runs
			s.policies = policies
		},
	}
}

// SeedCriteria stores criteria as if it had been created, keeping its ID and
// timestamps; a blank ID gets a new one and a zero CreatedAt now. It returns
// the stored criteria.
func (s *Store) SeedCriteria(criteria membercriteria.Criteria) membercriteria.Criteria {
	s.mu.Lock()
	defer s.mu.Unlock()
	if criteria.ID == "" {
		criteria.ID = uuid.NewString()
	}
	if criteria.CreatedAt.IsZero() {
		criteria.CreatedAt = now()
		criteria.UpdatedAt = &criteria.CreatedAt
	}
	criteria = storedCriteria(criteria)
	s.criteriaSet[criteria.ID] = criteria
	return cloneCriteria(criteria)
}

// Stored returns the stored criteria.
func (s *Store) Stored(id string) (membercriteria.Criteria, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria, ok := s.criteriaSet[id]
	return cloneCriteria(criteria), ok
}

// CriteriaCount counts the stored Member criteria.
func (s *Store) CriteriaCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.criteriaSet)
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

// SeedCampaign stores a campaign of the criteria and returns its ID; a
// scheduled campaign has a scheduled time.
func (s *Store) SeedCampaign(criteriaID, status string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.campaigns[id] = Campaign{CriteriaID: criteriaID, Status: status, Scheduled: status == CampaignScheduled}
	return id
}

// SeedDeliveryRun stores a delivery run of the criteria and returns its ID.
func (s *Store) SeedDeliveryRun(criteriaID, status string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := uuid.NewString()
	s.runs[id] = DeliveryRun{CriteriaID: criteriaID, Status: status}
	return id
}

// SeedDownloadPolicy stores a download policy; its CriteriaIDs are sorted.
func (s *Store) SeedDownloadPolicy(policy membercriteria.DownloadPolicy) membercriteria.DownloadPolicy {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy.CriteriaIDs = slices.Sorted(slices.Values(policy.CriteriaIDs))
	s.policies = append(s.policies, clonePolicies([]membercriteria.DownloadPolicy{policy})[0])
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

// PolicyCriteriaIDs returns the Member criteria the policy of itemID grants
// through.
func (s *Store) PolicyCriteriaIDs(itemID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, policy := range s.policies {
		if policy.ItemID == itemID {
			return slices.Clone(policy.CriteriaIDs)
		}
	}
	return nil
}

// Locks returns the lock methods called so far, in order: "campaign usage",
// "criteria" and "download policies".
func (s *Store) Locks() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.locks)
}

func (s *Store) Criteria(_ context.Context, id string) (membercriteria.Criteria, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria, ok := s.criteriaSet[id]
	if !ok {
		return membercriteria.Criteria{}, membercriteria.ErrCriteriaNotFound
	}
	return cloneCriteria(criteria), nil
}

func (s *Store) CriteriaArchived(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria, ok := s.criteriaSet[id]
	if !ok {
		return false, membercriteria.ErrCriteriaNotFound
	}
	return criteria.ArchivedAt != nil, nil
}

func (s *Store) LockCriteria(ctx context.Context, id string) (membercriteria.Criteria, error) {
	s.lock("criteria")
	return s.Criteria(ctx, id)
}

func (s *Store) ListCriteriaSet(_ context.Context, includeArchived bool, page membercriteria.Page) ([]membercriteria.Criteria, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(page, func(criteria membercriteria.Criteria) bool {
		return includeArchived || criteria.ArchivedAt == nil
	}, true)
}

func (s *Store) ListAuthenticatedAccessCriteriaSet(_ context.Context, page membercriteria.Page) ([]membercriteria.Criteria, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.list(page, func(criteria membercriteria.Criteria) bool {
		return criteria.ArchivedAt == nil && (criteria.Type == membercriteria.KindMemberTags || criteria.Type == membercriteria.KindFilter)
	}, false)
}

// list applies the search filter and the sorts the PostgreSQL store allows,
// with the same errors.
func (s *Store) list(page membercriteria.Page, include func(membercriteria.Criteria) bool, withConfig bool) ([]membercriteria.Criteria, int64, error) {
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
	var selected []membercriteria.Criteria
	for _, criteria := range s.criteriaSet {
		name := strings.ToLower(criteria.Name)
		if include(criteria) && !slices.ContainsFunc(search, func(value string) bool { return !strings.Contains(name, value) }) {
			selected = append(selected, cloneCriteria(criteria))
		}
	}
	compare, err := criteriaOrder(page.Sorts)
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
			selected[i].Config = membercriteria.Config{
				CreatedAfter: selected[i].Config.CreatedAfter, CreatedBefore: selected[i].Config.CreatedBefore,
			}
		}
	}
	return selected, total, nil
}

func criteriaOrder(sorts []*commonv1.SortSpec) (func(a, b membercriteria.Criteria) int, error) {
	if len(sorts) == 0 {
		return func(a, b membercriteria.Criteria) int {
			return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
		}, nil
	}
	var compares []func(a, b membercriteria.Criteria) int
	for _, sort := range sorts {
		var compare func(a, b membercriteria.Criteria) int
		switch sort.GetField() {
		case "name":
			compare = func(a, b membercriteria.Criteria) int { return cmp.Compare(a.Name, b.Name) }
		case "created_at":
			compare = func(a, b membercriteria.Criteria) int { return a.CreatedAt.Compare(b.CreatedAt) }
		case "kind":
			compare = func(a, b membercriteria.Criteria) int { return cmp.Compare(a.Type, b.Type) }
		default:
			return nil, errs.InvalidSortField(sort.GetField())
		}
		if sort.GetOrder() == commonv1.SortOrder_SORT_ORDER_DESC {
			ascending := compare
			compare = func(a, b membercriteria.Criteria) int { return -ascending(a, b) }
		}
		compares = append(compares, compare)
	}
	return func(a, b membercriteria.Criteria) int {
		for _, compare := range compares {
			if order := compare(a, b); order != 0 {
				return order
			}
		}
		return 0
	}, nil
}

func (s *Store) CriteriaReferences(_ context.Context, ids []string) (map[string]membercriteria.References, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	references := map[string]membercriteria.References{}
	for _, id := range ids {
		var counted membercriteria.References
		for _, campaign := range s.campaigns {
			if campaign.CriteriaID == id {
				counted.Campaigns++
			}
		}
		for _, run := range s.runs {
			if run.CriteriaID == id {
				counted.DeliveryRuns++
			}
		}
		for _, policy := range s.policies {
			if slices.Contains(policy.CriteriaIDs, id) {
				counted.DownloadPolicies++
			}
		}
		if counted != (membercriteria.References{}) {
			references[id] = counted
		}
	}
	return references, nil
}

func (s *Store) InsertCriteria(_ context.Context, criteria membercriteria.Criteria) (membercriteria.Criteria, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria.ID = uuid.NewString()
	criteria.CreatedAt = now()
	criteria.UpdatedAt = &criteria.CreatedAt
	criteria.ArchivedAt = nil
	stored := storedCriteria(criteria)
	stored.Config = membercriteria.Config{CreatedAfter: stored.Config.CreatedAfter, CreatedBefore: stored.Config.CreatedBefore}
	s.criteriaSet[criteria.ID] = stored
	created := cloneCriteria(stored)
	created.Config = cloneConfig(criteria.Config)
	return created, nil
}

func (s *Store) UpdateCriteria(_ context.Context, id string, change membercriteria.CriteriaChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria, ok := s.criteriaSet[id]
	if !ok {
		return nil
	}
	if change.Name != nil {
		criteria.Name = *change.Name
	}
	if change.Description != nil {
		description := *change.Description
		criteria.Description = &description
	}
	if change.Type != nil {
		criteria.Type = *change.Type
	}
	if change.SetCreatedAfter {
		criteria.Config.CreatedAfter = change.CreatedAfter
	}
	if change.SetCreatedBefore {
		criteria.Config.CreatedBefore = change.CreatedBefore
	}
	updatedAt := change.UpdatedAt
	criteria.UpdatedAt = &updatedAt
	s.criteriaSet[id] = storedCriteria(criteria)
	return nil
}

func (s *Store) ReplaceConfig(_ context.Context, id string, config membercriteria.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tagID := range config.MemberTagIDs {
		if !s.tags[tagID] {
			return membercriteria.ErrUnknownReference
		}
	}
	for _, memberID := range config.ExcludeMemberIDs {
		if _, exists := s.members[memberID]; !exists {
			return membercriteria.ErrUnknownReference
		}
	}
	criteria, ok := s.criteriaSet[id]
	if !ok {
		return nil
	}
	criteria.Config.MemberTagIDs = sortedOrNil(config.MemberTagIDs)
	criteria.Config.AccountRoles = sortedOrNil(config.AccountRoles)
	criteria.Config.ExcludeMemberIDs = sortedOrNil(config.ExcludeMemberIDs)
	s.criteriaSet[id] = criteria
	return nil
}

func (s *Store) ArchiveCriteria(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria, ok := s.criteriaSet[id]
	if !ok || criteria.ArchivedAt != nil {
		return membercriteria.ErrArchiveStateChanged
	}
	at = at.Truncate(time.Microsecond)
	criteria.ArchivedAt, criteria.UpdatedAt = &at, &at
	s.criteriaSet[id] = criteria
	return nil
}

func (s *Store) RestoreCriteria(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	criteria, ok := s.criteriaSet[id]
	if !ok || criteria.ArchivedAt == nil {
		return membercriteria.ErrArchiveStateChanged
	}
	at = at.Truncate(time.Microsecond)
	criteria.ArchivedAt, criteria.UpdatedAt = nil, &at
	s.criteriaSet[id] = criteria
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

func (s *Store) CriteriaInActiveDelivery(_ context.Context, id string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, campaign := range s.campaigns {
		if campaign.CriteriaID == id && (campaign.Status == CampaignScheduled || campaign.Status == CampaignSending) {
			return true, false, nil
		}
	}
	for _, run := range s.runs {
		if run.CriteriaID == id && (run.Status == RunScheduled || run.Status == RunSending) {
			return false, true, nil
		}
	}
	return false, false, nil
}

func (s *Store) LockCampaignUsage(_ context.Context, id string) (membercriteria.CampaignUsage, error) {
	s.lock("campaign usage")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage(id), nil
}

func (s *Store) CampaignUsageChanged(_ context.Context, id string, locked membercriteria.CampaignUsage) (bool, error) {
	if s.BeforeUsageCheck != nil {
		s.BeforeUsageCheck(s)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.usage(id)
	return !slices.Equal(current.CampaignIDs, locked.CampaignIDs) || !slices.Equal(current.DeliveryRunIDs, locked.DeliveryRunIDs), nil
}

func (s *Store) usage(id string) membercriteria.CampaignUsage {
	usage := membercriteria.CampaignUsage{CampaignIDs: []string{}, DeliveryRunIDs: []string{}}
	for campaignID, campaign := range s.campaigns {
		if campaign.CriteriaID == id {
			usage.CampaignIDs = append(usage.CampaignIDs, campaignID)
		}
	}
	for runID, run := range s.runs {
		if run.CriteriaID == id && (run.Status == RunScheduled || run.Status == RunSending) {
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
		if run.CriteriaID == id && run.Status == RunScheduled {
			run.Status, run.Completed = RunCancelled, true
			s.runs[runID] = run
		}
	}
	for campaignID, campaign := range s.campaigns {
		if campaign.CriteriaID == id && campaign.Status == CampaignScheduled {
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
		if slices.Contains(policy.CriteriaIDs, id) {
			return true, nil
		}
	}
	return false, nil
}

// LockDownloadPolicies returns the File attachment policies by item, then
// the Track policies by item, as the PostgreSQL store orders them.
func (s *Store) LockDownloadPolicies(_ context.Context, id string) ([]membercriteria.DownloadPolicy, error) {
	s.lock("download policies")
	s.mu.Lock()
	defer s.mu.Unlock()
	var policies []membercriteria.DownloadPolicy
	for _, policy := range s.policies {
		if slices.Contains(policy.CriteriaIDs, id) {
			policies = append(policies, policy)
		}
	}
	policies = clonePolicies(policies)
	slices.SortFunc(policies, func(a, b membercriteria.DownloadPolicy) int {
		return cmp.Or(cmp.Compare(boolOrder(a.Owner == "release"), boolOrder(b.Owner == "release")), cmp.Compare(a.ItemID, b.ItemID))
	})
	return policies, nil
}

func (s *Store) DetachDownloadPolicies(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.policies {
		s.policies[i].CriteriaIDs = slices.DeleteFunc(slices.Clone(s.policies[i].CriteriaIDs), func(criteriaID string) bool { return criteriaID == id })
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

// storedCriteria is criteria as PostgreSQL keeps it: times at microsecond
// precision.
func storedCriteria(criteria membercriteria.Criteria) membercriteria.Criteria {
	criteria = cloneCriteria(criteria)
	criteria.Config.CreatedAfter = truncated(criteria.Config.CreatedAfter)
	criteria.Config.CreatedBefore = truncated(criteria.Config.CreatedBefore)
	criteria.ArchivedAt = truncated(criteria.ArchivedAt)
	criteria.UpdatedAt = truncated(criteria.UpdatedAt)
	criteria.CreatedAt = criteria.CreatedAt.Truncate(time.Microsecond)
	return criteria
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

func cloneCriteria(criteria membercriteria.Criteria) membercriteria.Criteria {
	if criteria.Description != nil {
		description := *criteria.Description
		criteria.Description = &description
	}
	criteria.Config = cloneConfig(criteria.Config)
	criteria.ArchivedAt = clonedTime(criteria.ArchivedAt)
	criteria.UpdatedAt = clonedTime(criteria.UpdatedAt)
	return criteria
}

func clonedTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneConfig(config membercriteria.Config) membercriteria.Config {
	config.MemberTagIDs = slices.Clone(config.MemberTagIDs)
	config.AccountRoles = slices.Clone(config.AccountRoles)
	config.ExcludeMemberIDs = slices.Clone(config.ExcludeMemberIDs)
	config.CreatedAfter = clonedTime(config.CreatedAfter)
	config.CreatedBefore = clonedTime(config.CreatedBefore)
	return config
}

func clonePolicies(policies []membercriteria.DownloadPolicy) []membercriteria.DownloadPolicy {
	cloned := slices.Clone(policies)
	for i := range cloned {
		cloned[i].CriteriaIDs = slices.Clone(cloned[i].CriteriaIDs)
	}
	return cloned
}
