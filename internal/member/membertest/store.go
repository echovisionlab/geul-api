// Package membertest provides the in-memory Member Store, the Member
// authorization port over authztest.Checker, and the Store contract that
// both the fake and internal/member/postgres keep.
package membertest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"github.com/google/uuid"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Identity is a Kratos Identity as the Member store sees it.
type Identity struct {
	ExternalID string
	Inactive   bool
	Banned     bool
}

// Store is the in-memory member.Store. It knows no schema and takes no lock;
// the unit of work serializes its transactions. Its sentinels and orderings
// are the PostgreSQL store's.
type Store struct {
	mu    sync.Mutex
	state state
}

type state struct {
	members          map[string]member.Member
	identities       map[string]Identity
	pendingDeletions map[string]bool
	tags             map[string]member.Tag
	mappings         map[string]map[string]bool
	segmentTags      map[string]bool
	newsletter       map[string]time.Time
	consents         []member.CookieConsent
	assets           map[string]*memberref.Asset
	assetStatus      map[string]string
	bindings         map[string]string
	avatarSources    map[string]string
	tokens           map[string]int
	anchors          map[string]bool
	bootstrap        map[string]string
	deliveries       map[string]member.WelcomeDelivery
	welcome          []member.WelcomeEmail
	deletionRequests []string
	failures         map[string]error
}

// New returns an empty fake store and its unit of work.
func New() (*Store, *mutationtest.UnitOfWork[member.Store]) {
	store := &Store{state: state{
		members: map[string]member.Member{}, identities: map[string]Identity{}, pendingDeletions: map[string]bool{},
		tags: map[string]member.Tag{}, mappings: map[string]map[string]bool{}, segmentTags: map[string]bool{},
		newsletter: map[string]time.Time{}, assets: map[string]*memberref.Asset{}, assetStatus: map[string]string{},
		bindings: map[string]string{}, avatarSources: map[string]string{}, tokens: map[string]int{},
		anchors: map[string]bool{}, bootstrap: map[string]string{}, deliveries: map[string]member.WelcomeDelivery{},
		failures: map[string]error{},
	}}
	return store, mutationtest.New[member.Store](store)
}

var _ member.Store = (*Store)(nil)

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.state.clone()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		saved.failures = s.state.failures
		s.state = saved
	}
}

func cloneMember(value member.Member) member.Member {
	value.AvailableEmails = slices.Clone(value.AvailableEmails)
	value.SocialLinks = maps.Clone(value.SocialLinks)
	return value
}

func (st state) clone() state {
	members := make(map[string]member.Member, len(st.members))
	for id, value := range st.members {
		members[id] = cloneMember(value)
	}
	mappings := make(map[string]map[string]bool, len(st.mappings))
	for id, tags := range st.mappings {
		mappings[id] = maps.Clone(tags)
	}
	return state{
		members: members, identities: maps.Clone(st.identities), pendingDeletions: maps.Clone(st.pendingDeletions),
		tags: maps.Clone(st.tags), mappings: mappings, segmentTags: maps.Clone(st.segmentTags),
		newsletter: maps.Clone(st.newsletter), consents: slices.Clone(st.consents), assets: maps.Clone(st.assets),
		assetStatus: maps.Clone(st.assetStatus), bindings: maps.Clone(st.bindings), avatarSources: maps.Clone(st.avatarSources),
		tokens: maps.Clone(st.tokens), anchors: maps.Clone(st.anchors), bootstrap: maps.Clone(st.bootstrap),
		deliveries: maps.Clone(st.deliveries), welcome: slices.Clone(st.welcome),
		deletionRequests: slices.Clone(st.deletionRequests), failures: st.failures,
	}
}

// Fail makes every later call of the named Store method answer err.
func (s *Store) Fail(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.failures[method] = err
}

func (s *Store) failure(method string) error {
	return s.state.failures[method]
}

// SeedMember stores value as it is.
func (s *Store) SeedMember(value member.Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.members[value.ID] = cloneMember(value)
}

// SeedIdentity stores a Kratos Identity.
func (s *Store) SeedIdentity(identityID string, identity Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.identities[identityID] = identity
}

// SeedLinked stores an active Identity and an onboarded Member linked to it
// both ways, and returns the Member.
func (s *Store) SeedLinked(memberID, identityID, nickname string) member.Member {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	value := member.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: nickname, Onboarded: true,
		SocialLinks: map[string]string{}, CreatedAt: created, UpdatedAt: created,
	}
	s.SeedIdentity(identityID, Identity{ExternalID: memberID})
	s.SeedMember(value)
	return value
}

// SeedPendingDeletion marks the Member's account pending deletion.
func (s *Store) SeedPendingDeletion(memberID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.pendingDeletions[memberID] = true
}

// SeedTag stores a tag.
func (s *Store) SeedTag(tag member.Tag) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.tags[tag.ID] = tag
}

// SeedMemberTags assigns tags to a Member.
func (s *Store) SeedMemberTags(memberID string, tagIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.mappings[memberID] == nil {
		s.state.mappings[memberID] = map[string]bool{}
	}
	for _, tagID := range tagIDs {
		s.state.mappings[memberID][tagID] = true
	}
}

// SeedSegmentTag makes an audience segment use the tag.
func (s *Store) SeedSegmentTag(tagID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.segmentTags[tagID] = true
}

// SeedNewsletter subscribes an Identity.
func (s *Store) SeedNewsletter(identityID string, subscribedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.newsletter[identityID] = subscribedAt
}

// SeedAsset stores a public asset with its status.
func (s *Store) SeedAsset(asset *memberref.Asset, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.assets[asset.ID] = asset
	s.state.assetStatus[asset.ID] = status
}

// SeedAvatar binds a ready asset as the Member's avatar.
func (s *Store) SeedAvatar(memberID string, asset *memberref.Asset) {
	s.SeedAsset(asset, "ready")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.bindings[memberID] = asset.ID
}

// SeedAvatarSource records an avatar upload of ownerID.
func (s *Store) SeedAvatarSource(fileID, ownerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.avatarSources[fileID] = ownerID
}

// SeedPersonalAccessTokens gives the Member count tokens.
func (s *Store) SeedPersonalAccessTokens(memberID string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.tokens[memberID] = count
}

// SeedBootstrap records the Member under a bootstrap key.
func (s *Store) SeedBootstrap(key, memberID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.bootstrap[key] = memberID
}

// SeedWelcomeDelivery answers WelcomeDelivery for identityID.
func (s *Store) SeedWelcomeDelivery(identityID string, delivery member.WelcomeDelivery) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.deliveries[identityID] = delivery
}

// Member reads a stored Member.
func (s *Store) Member(memberID string) (member.Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	return cloneMember(value), found
}

// Tags returns the stored tags.
func (s *Store) Tags() map[string]member.Tag {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.state.tags)
}

// MemberTags returns the Member's tags, sorted.
func (s *Store) MemberTags(memberID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.state.mappings[memberID])
}

// Consents returns the recorded cookie consents.
func (s *Store) Consents() []member.CookieConsent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.consents)
}

// Newsletter reports when identityID subscribed.
func (s *Store) Newsletter(identityID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, found := s.state.newsletter[identityID]
	return at, found
}

// AvatarBinding returns the asset bound as the Member's avatar.
func (s *Store) AvatarBinding(memberID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assetID, found := s.state.bindings[memberID]
	return assetID, found
}

// AssetStatus returns a public asset's status.
func (s *Store) AssetStatus(assetID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.assetStatus[assetID]
}

// PersonalAccessTokens returns how many tokens the Member keeps.
func (s *Store) PersonalAccessTokens(memberID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.tokens[memberID]
}

// Anchors returns the account identity anchors.
func (s *Store) Anchors() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.state.anchors)
}

// Bootstrap returns the Member recorded under a bootstrap key.
func (s *Store) Bootstrap(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	memberID, found := s.state.bootstrap[key]
	return memberID, found
}

// WelcomeEmails returns the enqueued welcome commands.
func (s *Store) WelcomeEmails() []member.WelcomeEmail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.welcome)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (st state) linkedIdentity(value member.Member) (Identity, bool) {
	if value.AccountIdentityID == nil {
		return Identity{}, false
	}
	identity, found := st.identities[*value.AccountIdentityID]
	return identity, found && identity.ExternalID == value.ID
}

func (s *Store) Members(_ context.Context, ids []string) ([]member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("Members"); err != nil {
		return nil, err
	}
	var result []member.Member
	for _, id := range sortedKeys(s.state.members) {
		if len(ids) == 0 || slices.Contains(ids, id) {
			result = append(result, cloneMember(s.state.members[id]))
		}
	}
	return result, nil
}

func (s *Store) find(memberID string, match func(member.Member) bool) (member.Member, error) {
	value, found := s.state.members[memberID]
	if !found || !match(value) {
		return member.Member{}, member.ErrMemberNotFound
	}
	return cloneMember(value), nil
}

func (s *Store) MemberByID(_ context.Context, memberID string) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(memberID, func(member.Member) bool { return true })
}

func (s *Store) SessionMember(ctx context.Context, memberID string) (member.Member, error) {
	value, err := s.MemberByID(ctx, memberID)
	if err != nil {
		return member.Member{}, err
	}
	return member.Member{ID: value.ID, AccountIdentityID: value.AccountIdentityID, Nickname: value.Nickname, Onboarded: value.Onboarded, PreferredLocale: value.PreferredLocale, DeletedAt: value.DeletedAt}, nil
}

func (s *Store) OwnMember(_ context.Context, memberID string) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(memberID, func(value member.Member) bool {
		return value.DeletedAt == nil && value.AccountIdentityID != nil && value.Onboarded
	})
}

func (s *Store) SettingsMember(_ context.Context, memberID string) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(memberID, func(value member.Member) bool { return value.DeletedAt == nil && value.AccountIdentityID != nil })
}

func (s *Store) LinkedMember(_ context.Context, memberID, identityID string) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.find(memberID, func(value member.Member) bool {
		return value.DeletedAt == nil && value.AccountIdentityID != nil && *value.AccountIdentityID == identityID
	})
}

func (s *Store) SearchMembers(_ context.Context, search member.MemberSearch) ([]member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []member.Member
	for _, value := range s.state.members {
		if value.DeletedAt != nil || value.AccountIdentityID == nil || !value.Onboarded {
			continue
		}
		if search.IdentityIDs != nil && !slices.Contains(search.IdentityIDs, *value.AccountIdentityID) {
			continue
		}
		if search.Query != "" && !strings.Contains(strings.ToLower(value.Nickname), strings.ToLower(search.Query)) {
			continue
		}
		if slices.Contains(search.ExcludeMemberIDs, value.ID) {
			continue
		}
		result = append(result, cloneMember(value))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Nickname != result[j].Nickname {
			return result[i].Nickname < result[j].Nickname
		}
		return result[i].ID < result[j].ID
	})
	if len(result) > search.Limit {
		result = result[:search.Limit]
	}
	return result, nil
}

// AdminMemberPage implements member.Store with the default order only; the
// filters and sorts are the PostgreSQL store's and its tests'.
func (s *Store) AdminMemberPage(_ context.Context, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset int) ([]member.Member, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("AdminMemberPage"); err != nil {
		return nil, 0, err
	}
	if len(filters) != 0 || len(sorts) != 0 {
		return nil, 0, errors.New("membertest: the fake store pages Members in the default order only")
	}
	all := make([]member.Member, 0, len(s.state.members))
	for _, value := range s.state.members {
		all = append(all, cloneMember(value))
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	total := int64(len(all))
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}

func (s *Store) Avatars(_ context.Context, memberIDs []string) (map[string]*memberref.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("Avatars"); err != nil {
		return nil, err
	}
	result := make(map[string]*memberref.Asset, len(memberIDs))
	for _, memberID := range memberIDs {
		assetID, found := s.state.bindings[memberID]
		if found && s.state.assetStatus[assetID] == "ready" {
			result[memberID] = s.state.assets[assetID]
		}
	}
	return result, nil
}

func (s *Store) NewsletterSubscriptions(_ context.Context, memberIDs []string) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]time.Time, len(memberIDs))
	for _, memberID := range memberIDs {
		value, found := s.state.members[memberID]
		if !found {
			continue
		}
		if _, linked := s.state.linkedIdentity(value); !linked {
			continue
		}
		if at, subscribed := s.state.newsletter[*value.AccountIdentityID]; subscribed {
			result[memberID] = at
		}
	}
	return result, nil
}

func (s *Store) LatestCookieConsent(_ context.Context, memberID string) (*member.CookieConsent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *member.CookieConsent
	for i := range s.state.consents {
		consent := s.state.consents[i]
		if consent.MemberID == memberID && (latest == nil || consent.RecordedAt.After(latest.RecordedAt)) {
			latest = &consent
		}
	}
	return latest, nil
}

func (s *Store) NicknameClaimed(_ context.Context, nickname, exceptMemberID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.state.members {
		if value.Nickname == nickname && value.ID != exceptMemberID && value.DeletedAt == nil {
			return true, nil
		}
	}
	return false, nil
}

func (st state) target(memberID string, requireOnboarded, requireActive bool) (member.Target, error) {
	value, found := st.members[memberID]
	if !found || value.DeletedAt != nil || (requireOnboarded && !value.Onboarded) {
		return member.Target{}, member.ErrIneligible
	}
	identity, linked := st.linkedIdentity(value)
	if !linked || value.ID == *value.AccountIdentityID {
		return member.Target{}, member.ErrIneligible
	}
	if requireActive && (identity.Inactive || identity.Banned || st.pendingDeletions[memberID]) {
		return member.Target{}, member.ErrIneligible
	}
	return member.Target{MemberID: value.ID, IdentityID: *value.AccountIdentityID, Onboarded: value.Onboarded}, nil
}

func requireAnswer(memberID string, target member.Target, err error) (member.Target, error) {
	if errors.Is(err, member.ErrIneligible) {
		return member.Target{}, domainerr.ResourceNotFound("member", memberID)
	}
	return target, err
}

func (s *Store) RequireTarget(_ context.Context, memberID string) (member.Target, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return member.Target{}, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, err := s.state.target(memberID, true, true)
	return requireAnswer(memberID, target, err)
}

func (s *Store) RequireLinkedTarget(_ context.Context, memberID string, requireOnboarded bool) (member.Target, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return member.Target{}, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, err := s.state.target(memberID, requireOnboarded, false)
	return requireAnswer(memberID, target, err)
}

func (s *Store) CurrentTarget(_ context.Context, memberID string) (member.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.target(memberID, true, true)
}

func (s *Store) CurrentLinkedTarget(_ context.Context, memberID string, requireOnboarded bool) (member.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.target(memberID, requireOnboarded, false)
}

func (st state) nicknameTaken(memberID, nickname string) bool {
	for _, other := range st.members {
		if other.ID != memberID && other.Nickname == nickname {
			return true
		}
	}
	return false
}

func (s *Store) UpdateProfile(_ context.Context, memberID string, update member.ProfileUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("UpdateProfile"); err != nil {
		return err
	}
	value, found := s.state.members[memberID]
	if !found {
		return nil
	}
	if update.Nickname != nil {
		if s.state.nicknameTaken(memberID, *update.Nickname) {
			return member.ErrNicknameTaken
		}
		value.Nickname = *update.Nickname
	}
	if update.Bio.Set {
		value.Bio = update.Bio.Value
	}
	if update.Website.Set {
		value.Website = update.Website.Value
	}
	if update.SocialLinks != nil {
		value.SocialLinks = maps.Clone(update.SocialLinks)
	}
	value.UpdatedAt = update.UpdatedAt
	s.state.members[memberID] = value
	return nil
}

func (s *Store) CompleteOnboarding(_ context.Context, memberID, identityID, nickname string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	if !found || value.AccountIdentityID == nil || *value.AccountIdentityID != identityID || value.DeletedAt != nil || value.Onboarded {
		return false, nil
	}
	if s.state.nicknameTaken(memberID, nickname) {
		return false, member.ErrNicknameTaken
	}
	value.Nickname, value.Onboarded, value.UpdatedAt = nickname, true, now
	s.state.members[memberID] = value
	return true, nil
}

func (s *Store) UpdatePreferredLocale(_ context.Context, memberID, locale string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, found := s.state.members[memberID]; found {
		value.PreferredLocale, value.UpdatedAt = &locale, now
		s.state.members[memberID] = value
	}
	return nil
}

func (s *Store) InsertCookieConsent(_ context.Context, consent member.CookieConsent) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("InsertCookieConsent"); err != nil {
		return "", err
	}
	consent.ID = uuid.NewString()
	s.state.consents = append(s.state.consents, consent)
	return consent.ID, nil
}

func (s *Store) TagIDs(_ context.Context, memberIDs []string) (map[string][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string][]string, len(memberIDs))
	for _, memberID := range memberIDs {
		if tags := sortedKeys(s.state.mappings[memberID]); len(tags) != 0 {
			result[memberID] = tags
		}
	}
	return result, nil
}

// TagPage implements member.Store in name order without filters; the
// filters and sorts are the PostgreSQL store's and its tests'.
func (s *Store) TagPage(_ context.Context, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset int) ([]member.Tag, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(filters) != 0 || len(sorts) != 0 {
		return nil, 0, errors.New("membertest: the fake store pages tags in name order only")
	}
	tags := make([]member.Tag, 0, len(s.state.tags))
	for _, tag := range s.state.tags {
		count := int32(0)
		for _, assigned := range s.state.mappings {
			if assigned[tag.ID] {
				count++
			}
		}
		tag.MemberCount = count
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Name != tags[j].Name {
			return tags[i].Name < tags[j].Name
		}
		return tags[i].ID < tags[j].ID
	})
	total := int64(len(tags))
	if offset > len(tags) {
		offset = len(tags)
	}
	tags = tags[offset:]
	if len(tags) > limit {
		tags = tags[:limit]
	}
	return tags, total, nil
}

func (s *Store) InsertTag(_ context.Context, name string) (member.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tag := range s.state.tags {
		if tag.Name == name {
			return member.Tag{}, member.ErrTagNameTaken
		}
	}
	tag := member.Tag{ID: uuid.NewString(), Name: name, CreatedAt: time.Now()}
	s.state.tags[tag.ID] = tag
	return tag, nil
}

func (s *Store) LockTag(_ context.Context, tagID string) (member.Tag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tag, found := s.state.tags[tagID]
	if !found {
		return member.Tag{}, member.ErrTagNotFound
	}
	return tag, nil
}

func (s *Store) TagReferencedBySegment(_ context.Context, tagID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.segmentTags[tagID], nil
}

func (s *Store) DeleteTag(_ context.Context, tagID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, assigned := range s.state.mappings {
		delete(assigned, tagID)
	}
	delete(s.state.tags, tagID)
	return nil
}

func (s *Store) CountTags(_ context.Context, tagIDs []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for _, tagID := range slices.Compact(slices.Sorted(slices.Values(tagIDs))) {
		if _, found := s.state.tags[tagID]; found {
			count++
		}
	}
	return count, nil
}

func (s *Store) ReplaceMemberTags(_ context.Context, memberID string, tagIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	assigned := make(map[string]bool, len(tagIDs))
	for _, tagID := range tagIDs {
		if _, found := s.state.tags[tagID]; !found {
			return fmt.Errorf("membertest: user_tag_mapping references unknown tag %s", tagID)
		}
		assigned[tagID] = true
	}
	s.state.mappings[memberID] = assigned
	return nil
}

func (s *Store) AvatarSourceOwned(_ context.Context, fileID, ownerID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, found := s.state.avatarSources[fileID]
	return found && (ownerID == "" || owner == ownerID), nil
}

func (s *Store) LockAvatarBinding(_ context.Context, memberID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	assetID, found := s.state.bindings[memberID]
	return assetID, found, nil
}

func (s *Store) BindAvatar(_ context.Context, memberID, assetID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("BindAvatar"); err != nil {
		return err
	}
	if _, found := s.state.assetStatus[assetID]; !found {
		s.state.assetStatus[assetID] = "ready"
	}
	s.state.bindings[memberID] = assetID
	return nil
}

func (s *Store) ReleaseAvatar(_ context.Context, memberID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.bindings, memberID)
	return nil
}

func (s *Store) WelcomeDelivery(_ context.Context, identityID string) (member.WelcomeDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("WelcomeDelivery"); err != nil {
		return member.WelcomeDelivery{}, err
	}
	return s.state.deliveries[identityID], nil
}

func (s *Store) EnqueueWelcomeEmail(_ context.Context, email member.WelcomeEmail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("EnqueueWelcomeEmail"); err != nil {
		return err
	}
	s.state.welcome = append(s.state.welcome, email)
	return nil
}

func (s *Store) LockRegistrationIdentity(_ context.Context, identityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, collides := s.state.members[identityID]; collides {
		return errors.New("identity_id collides with an existing member_id")
	}
	return nil
}

func (s *Store) LockRegistrationMember(_ context.Context, identityID string) (member.Member, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, value := range s.state.members {
		if value.AccountIdentityID != nil && *value.AccountIdentityID == identityID {
			return cloneMember(value), true, nil
		}
	}
	return member.Member{}, false, nil
}

func (s *Store) MemberIDOccupied(_ context.Context, candidate string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.state.members[candidate]; found {
		return true, nil
	}
	if _, found := s.state.identities[candidate]; found {
		return true, nil
	}
	return s.state.nicknameTaken("", candidate), nil
}

func (s *Store) CreateRegistrationMember(_ context.Context, created member.Member) (member.Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.anchors[*created.AccountIdentityID] = true
	now := time.Now().UTC()
	created.CreatedAt, created.UpdatedAt = now, now
	s.state.members[created.ID] = cloneMember(created)
	return created, nil
}

// EmailReuseHeld implements member.Store: a deleted Member holds its emails
// for seven days after deletion, an unlinked live one until it is deleted.
func (s *Store) EmailReuseHeld(_ context.Context, email string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	holdStart := time.Now().Add(-7 * 24 * time.Hour)
	for _, value := range s.state.members {
		if value.AccountIdentityID == nil && slices.Contains(value.AvailableEmails, email) &&
			(value.DeletedAt == nil || value.DeletedAt.After(holdStart)) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) LinkedMemberExists(_ context.Context, memberID, identityID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	return found && value.DeletedAt == nil && value.AccountIdentityID != nil && *value.AccountIdentityID == identityID, nil
}

func (s *Store) SubscribeNewsletter(_ context.Context, identityID string, subscribedAt time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.state.newsletter[identityID]; found {
		return false, nil
	}
	s.state.newsletter[identityID] = subscribedAt
	return true, nil
}

func (s *Store) UnsubscribeNewsletter(_ context.Context, identityID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.state.newsletter[identityID]
	delete(s.state.newsletter, identityID)
	return found, nil
}

func (s *Store) NewsletterSubscription(_ context.Context, identityID string) (*time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failure("NewsletterSubscription"); err != nil {
		return nil, err
	}
	at, found := s.state.newsletter[identityID]
	if !found {
		return nil, nil
	}
	return &at, nil
}

func (s *Store) linkedPair(memberID, identityID string) (member.Member, bool) {
	value, found := s.state.members[memberID]
	if !found || value.DeletedAt != nil || value.AccountIdentityID == nil || *value.AccountIdentityID != identityID {
		return member.Member{}, false
	}
	_, linked := s.state.linkedIdentity(value)
	return value, linked
}

func (s *Store) PrimaryEmail(_ context.Context, memberID, identityID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.linkedPair(memberID, identityID)
	if !found || value.PrimaryEmail == nil {
		return "", member.ErrMemberNotFound
	}
	return *value.PrimaryEmail, nil
}

func (s *Store) LockLinkedMember(_ context.Context, memberID, identityID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.linkedPair(memberID, identityID)
	if !found {
		return "", member.ErrMemberNotFound
	}
	return value.ID, nil
}

func (s *Store) UpdateEmailProjection(_ context.Context, memberID, identityID, primaryEmail string, availableEmails []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.state.members[memberID]
	if !found || value.DeletedAt != nil || value.AccountIdentityID == nil || *value.AccountIdentityID != identityID {
		return nil
	}
	value.PrimaryEmail, value.AvailableEmails = &primaryEmail, slices.Clone(availableEmails)
	s.state.members[memberID] = value
	return nil
}
