// Package accounttest provides the in-memory Account Store and units of
// work, the Account authorization and Kratos ports over in-memory state, and
// the Store contract that both the fake and internal/account/postgres keep.
package accounttest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Member is a Member row as the Account store sees it.
type Member struct {
	IdentityID      string
	Nickname        string
	Onboarded       bool
	Deleted         bool
	PrimaryEmail    string
	AvailableEmails []string
	Locale          *string
	AvatarAssetID   string
	CreatedAt       time.Time
	// Unlinked keeps the Member's reverse pointer while its Identity no
	// longer names it.
	Unlinked bool
}

// Identity is the Kratos Identity row the Account store reads.
type Identity struct {
	ExternalID    string
	State         string
	Banned        bool
	Email         string
	EmailVerified bool
	BanReason     *string
	BanExpires    *time.Time
	UpdatedAt     time.Time
	// LiveSessions are its live sessions.
	LiveSessions []account.Session
}

// Store is the in-memory account.Store. It knows no schema and takes no
// lock; the units of work serialize its transactions. Its sentinels and
// orderings are the PostgreSQL store's.
type Store struct {
	mu    sync.Mutex
	state state
}

type state struct {
	members      map[string]Member
	identities   map[string]Identity
	deletions    map[string]account.DeletionRequest
	emailChanges map[string]account.EmailChangeRequest
	newsletter   map[string]time.Time
	anchors      map[string]bool
	identityCmds []account.DeleteIdentityCommand
	avatarCmds   []account.DeleteAvatarCommand
	finalized    []string
	locked       []string
	failures     map[string]error
	hooks        map[string]func()
}

// New returns an empty fake store and its units of work.
func New() (*Store, *Units) {
	store := &Store{state: state{
		members: map[string]Member{}, identities: map[string]Identity{}, deletions: map[string]account.DeletionRequest{},
		emailChanges: map[string]account.EmailChangeRequest{}, newsletter: map[string]time.Time{}, anchors: map[string]bool{},
		failures: map[string]error{}, hooks: map[string]func(){},
	}}
	return store, newUnits(store)
}

var _ account.Store = (*Store)(nil)

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := s.state.clone()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"members":      saved.members,
			"identities":   saved.identities,
			"deletions":    saved.deletions,
			"emailChanges": saved.emailChanges,
			"newsletter":   saved.newsletter,
			"anchors":      saved.anchors,
			"identityCmds": saved.identityCmds,
			"avatarCmds":   saved.avatarCmds,
			"finalized":    saved.finalized,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			saved.failures, saved.hooks = s.state.failures, s.state.hooks
			s.state = saved
		},
	}
}

func (st state) clone() state {
	clone := st
	clone.members = mutationtest.ClonePersisted(st.members)
	clone.identities = mutationtest.ClonePersisted(st.identities)
	clone.deletions = mutationtest.ClonePersisted(st.deletions)
	clone.emailChanges = mutationtest.ClonePersisted(st.emailChanges)
	clone.newsletter = mutationtest.ClonePersisted(st.newsletter)
	clone.anchors = mutationtest.ClonePersisted(st.anchors)
	clone.identityCmds = mutationtest.ClonePersisted(st.identityCmds)
	clone.avatarCmds = mutationtest.ClonePersisted(st.avatarCmds)
	clone.finalized = mutationtest.ClonePersisted(st.finalized)
	clone.locked = slices.Clone(st.locked)
	return clone
}

// Fail makes every later call of the named Store method answer err; nil
// clears it.
func (s *Store) Fail(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.state.failures, method)
		return
	}
	s.state.failures[method] = err
}

// OnCall runs hook, outside the store's lock, before every later call of
// the named Store method; a hook plays a concurrent writer.
func (s *Store) OnCall(method string, hook func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.hooks[method] = hook
}

// enter runs the method's hook and answers its failure.
func (s *Store) enter(method string) error {
	s.mu.Lock()
	hook := s.state.hooks[method]
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.failures[method]
}

// SeedMember stores a Member.
func (s *Store) SeedMember(memberID string, member Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member.AvailableEmails = slices.Clone(member.AvailableEmails)
	s.state.members[memberID] = member
}

// SeedIdentity stores a Kratos Identity.
func (s *Store) SeedIdentity(identityID string, identity Identity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.identities[identityID] = identity
}

// SeedPair stores an active Identity and an onboarded Member linked both
// ways, with email as its canonical and only Member email.
func (s *Store) SeedPair(memberID, identityID, email string) {
	s.SeedIdentity(identityID, Identity{ExternalID: memberID, State: auth.KratosStateActive, Email: email, EmailVerified: true})
	s.SeedMember(memberID, Member{IdentityID: identityID, Nickname: "member-" + memberID[:8], Onboarded: true, PrimaryEmail: email, AvailableEmails: []string{email}})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.anchors[identityID] = true
}

// SeedDeletionRequest stores a deletion request.
func (s *Store) SeedDeletionRequest(request account.DeletionRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.deletions[request.ID] = request
}

// SeedEmailChange stores an email change request.
func (s *Store) SeedEmailChange(request account.EmailChangeRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.emailChanges[request.ID] = request
}

// SeedNewsletter subscribes an Identity.
func (s *Store) SeedNewsletter(identityID string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.newsletter[identityID] = at
}

// Member reads a stored Member.
func (s *Store) Member(memberID string) (Member, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	return member, found
}

// UpdateMember changes a stored Member.
func (s *Store) UpdateMember(memberID string, change func(*Member)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	member := s.state.members[memberID]
	change(&member)
	s.state.members[memberID] = member
}

// UpdateIdentity changes a stored Identity.
func (s *Store) UpdateIdentity(identityID string, change func(*Identity)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity := s.state.identities[identityID]
	change(&identity)
	s.state.identities[identityID] = identity
}

// DeleteIdentity removes a Kratos Identity and clears its Member's reverse
// pointer, as Kratos' identity deletion does through the schema.
func (s *Store) DeleteIdentity(identityID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.identities, identityID)
	for id, member := range s.state.members {
		if member.IdentityID == identityID {
			member.IdentityID = ""
			s.state.members[id] = member
		}
	}
}

// DeletionRequests returns the stored deletion requests by ID.
func (s *Store) DeletionRequests() map[string]account.DeletionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.state.deletions)
}

// EmailChanges returns the stored email change requests by ID.
func (s *Store) EmailChanges() map[string]account.EmailChangeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.state.emailChanges)
}

// Newsletter reports whether identityID is subscribed.
func (s *Store) Newsletter(identityID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.state.newsletter[identityID]
	return found
}

// Anchor reports whether the account identity anchor exists.
func (s *Store) Anchor(identityID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.anchors[identityID]
}

// IdentityCommands returns the enqueued identity deletion commands.
func (s *Store) IdentityCommands() []account.DeleteIdentityCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.identityCmds)
}

// AvatarCommands returns the enqueued avatar deletion commands.
func (s *Store) AvatarCommands() []account.DeleteAvatarCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.avatarCmds)
}

// Finalized returns the Members whose terminal deletion step committed.
func (s *Store) Finalized() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.finalized)
}

func (st state) linked(memberID string) (Member, Identity, bool) {
	member, found := st.members[memberID]
	if !found || member.IdentityID == "" || member.Unlinked {
		return member, Identity{}, false
	}
	identity, exists := st.identities[member.IdentityID]
	return member, identity, exists && identity.ExternalID == memberID && memberID != member.IdentityID
}

func (st state) pendingDeletion(memberID, identityID string) bool {
	for _, request := range st.deletions {
		if request.MemberID == memberID && request.IdentityID == identityID &&
			(request.LifecycleState == "scheduled" || request.LifecycleState == "recovery_confirmation_pending") {
			return true
		}
	}
	return false
}

func activeIdentity(identity Identity) bool {
	return identity.State == auth.KratosStateActive && !identity.Banned
}

// target answers authorizationtarget's resolution of a Member target.
func (st state) target(memberID string, requireOnboarded, requireActive bool) (account.Target, error) {
	member, identity, linked := st.linked(memberID)
	if !linked || member.Deleted || (requireOnboarded && !member.Onboarded) {
		return account.Target{}, account.ErrIneligible
	}
	if requireActive && (!activeIdentity(identity) || st.pendingDeletion(memberID, member.IdentityID)) {
		return account.Target{}, account.ErrIneligible
	}
	return account.Target{MemberID: memberID, IdentityID: member.IdentityID, Onboarded: member.Onboarded}, nil
}

func requireAnswer(memberID string, target account.Target, err error) (account.Target, error) {
	if _, parseErr := uuidutil.ParseCanonical(memberID, "member_id"); parseErr != nil {
		return account.Target{}, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	if errors.Is(err, account.ErrIneligible) {
		return account.Target{}, domainerr.ResourceNotFound("member", memberID)
	}
	return target, err
}

func (s *Store) RequireLinkedMember(_ context.Context, memberID string, requireOnboarded bool) (account.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, err := s.state.target(memberID, requireOnboarded, false)
	return requireAnswer(memberID, target, err)
}

func (s *Store) RequireLive(_ context.Context, memberID string) (account.Target, error) {
	if err := s.enter("RequireLive"); err != nil {
		return account.Target{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, err := s.state.target(memberID, false, true)
	return requireAnswer(memberID, target, err)
}

func (s *Store) LinkedMemberForMember(_ context.Context, memberID string, requireOnboarded bool) (account.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.target(memberID, requireOnboarded, false)
}

// activeForIdentity answers authorizationtarget.ActiveForIdentity.
func (st state) activeForIdentity(identityID string) (account.Target, bool) {
	for memberID, member := range st.members {
		if member.IdentityID != identityID {
			continue
		}
		_, identity, linked := st.linked(memberID)
		if linked && !member.Deleted && activeIdentity(identity) {
			return account.Target{MemberID: memberID, IdentityID: identityID, Onboarded: member.Onboarded}, true
		}
	}
	return account.Target{}, false
}

func (s *Store) ValidateActivePair(_ context.Context, memberID, identityID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	if memberID == identityID {
		return errors.New("identity_id and member_id must be distinct")
	}
	if err := s.enter("ValidateActivePair"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, found := s.state.activeForIdentity(identityID)
	if !found || target.MemberID != memberID {
		return account.ErrMemberLinkNotFound
	}
	return nil
}

func (s *Store) ActiveMemberIDForIdentity(_ context.Context, identityID string) (string, error) {
	if err := s.enter("ActiveMemberIDForIdentity"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	target, found := s.state.activeForIdentity(identityID)
	if !found {
		return "", account.ErrMemberLinkNotFound
	}
	return target.MemberID, nil
}

func (s *Store) ActiveOnboardedMemberForIdentity(_ context.Context, identityID string) (account.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, found := s.state.activeForIdentity(identityID)
	if !found || !target.Onboarded {
		return account.Target{}, account.ErrMemberLinkNotFound
	}
	return target, nil
}

func (s *Store) LockActivePairForCredentialMutation(_ context.Context, memberID, identityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target, found := s.state.activeForIdentity(identityID)
	if !found || target.MemberID != memberID || !target.Onboarded {
		return account.ErrMemberLinkNotFound
	}
	return nil
}

func (s *Store) LockIdentity(_ context.Context, identityID string) error {
	if strings.TrimSpace(identityID) == "" {
		return errors.New("user identity state fence requires user id")
	}
	if err := s.enter("LockIdentity"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.locked = append(s.state.locked, identityID)
	return nil
}

// LockedIdentities returns the Identities whose fence a committed
// transaction took, in order.
func (s *Store) LockedIdentities() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.state.locked)
}

func (s *Store) LockedIdentityState(_ context.Context, identityID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.identities[identityID].State, nil
}

func (s *Store) IdentityExists(_ context.Context, identityID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.state.identities[identityID]
	return exists, nil
}

func (st state) summaryRow(memberID string, member Member) account.SummaryRow {
	row := account.SummaryRow{MemberID: memberID}
	if member.Deleted {
		now := time.Now()
		row.DeletedAt = &now
	}
	_, identity, linked := st.linked(memberID)
	if linked {
		identityID, state := member.IdentityID, identity.State
		row.IdentityID, row.IdentityState = &identityID, &state
		if email := strings.TrimSpace(identity.Email); email != "" {
			row.Email = &email
		}
		row.EmailVerified = identity.EmailVerified
		row.MetadataBanned, row.BanReason, row.BanExpires = identity.Banned, identity.BanReason, identity.BanExpires
	}
	for _, request := range st.deletions {
		if request.MemberID == memberID && request.ScheduledAt != nil &&
			(request.LifecycleState == "scheduled" || request.LifecycleState == "recovery_confirmation_pending") {
			row.PendingDelete = true
		}
	}
	return row
}

func (s *Store) AccountSummaries(_ context.Context, memberIDs []string) ([]account.SummaryRow, error) {
	if err := s.enter("AccountSummaries"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []account.SummaryRow
	for _, memberID := range memberIDs {
		if member, found := s.state.members[memberID]; found {
			rows = append(rows, s.state.summaryRow(memberID, member))
		}
	}
	return rows, nil
}

func (s *Store) SessionSummary(_ context.Context, memberID string) (account.SummaryRow, bool, error) {
	if err := s.enter("SessionSummary"); err != nil {
		return account.SummaryRow{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, found := s.state.members[memberID]
	if !found {
		return account.SummaryRow{}, false, nil
	}
	row := s.state.summaryRow(memberID, member)
	row.EmailVerified, row.BanReason, row.BanExpires = false, nil, nil
	return row, true, nil
}

func (s *Store) LiveSessions(_ context.Context, identityID string) ([]account.Session, error) {
	if err := s.enter("LiveSessions"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions := slices.Clone(s.state.identities[identityID].LiveSessions)
	sort.Slice(sessions, func(i, j int) bool {
		if !sessions[i].AuthenticatedAt.Equal(sessions[j].AuthenticatedAt) {
			return sessions[i].AuthenticatedAt.After(sessions[j].AuthenticatedAt)
		}
		return sessions[i].ID > sessions[j].ID
	})
	return sessions, nil
}

func (s *Store) OwnsLiveSession(_ context.Context, identityID, sessionID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.state.identities[identityID].LiveSessions {
		if session.ID == sessionID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) OtherLiveSessionIDs(_ context.Context, identityID, currentSessionID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, session := range s.state.identities[identityID].LiveSessions {
		if session.ID != currentSessionID {
			ids = append(ids, session.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) DeletionRequestIdentityByToken(_ context.Context, tokenHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.state.deletions {
		if request.Token == tokenHash {
			return request.IdentityID, nil
		}
	}
	return "", account.ErrDeletionRequestNotFound
}

func (s *Store) LockDeletionRequestByToken(_ context.Context, tokenHash string, now time.Time, states []string) (account.DeletionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.state.deletions {
		if request.Token == tokenHash && request.TokenExpiresAt.After(now) && slices.Contains(states, request.LifecycleState) {
			return request, nil
		}
	}
	return account.DeletionRequest{}, account.ErrDeletionRequestNotFound
}

func (s *Store) LockPairDeletionRequest(_ context.Context, memberID, identityID string) (account.DeletionRequest, bool, error) {
	if err := s.enter("LockPairDeletionRequest"); err != nil {
		return account.DeletionRequest{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.state.deletions {
		if request.MemberID == memberID && request.IdentityID == identityID {
			return request, true, nil
		}
	}
	return account.DeletionRequest{}, false, nil
}

func (s *Store) DeleteDeletionRequest(_ context.Context, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.deletions, requestID)
	return nil
}

func (s *Store) DeletePairDeletionRequests(_ context.Context, memberID, identityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, request := range s.state.deletions {
		if request.MemberID == memberID && request.IdentityID == identityID {
			delete(s.state.deletions, id)
		}
	}
	return nil
}

func (s *Store) InsertDeletionRequest(_ context.Context, request account.DeletionRequest) error {
	if err := s.enter("InsertDeletionRequest"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.state.deletions {
		if existing.Token == request.Token {
			return errors.New("accounttest: duplicate user_deletion_request token")
		}
	}
	s.state.deletions[request.ID] = request
	return nil
}

func (s *Store) updateDeletion(requestID string, change func(*account.DeletionRequest)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request, found := s.state.deletions[requestID]; found {
		change(&request)
		s.state.deletions[requestID] = request
	}
}

func (s *Store) ScheduleDeletionRequest(_ context.Context, requestID string, now, scheduledAt time.Time) error {
	s.updateDeletion(requestID, func(request *account.DeletionRequest) {
		request.ConfirmedAt, request.ScheduledAt, request.TokenExpiresAt = &now, &scheduledAt, scheduledAt
		request.LifecycleState, request.UpdatedAt = "scheduled", now
	})
	return nil
}

func (s *Store) EndDeletionRequest(_ context.Context, requestID, state string) error {
	s.updateDeletion(requestID, func(request *account.DeletionRequest) {
		request.ScheduledAt, request.LifecycleState = nil, state
		request.UpdatedAt = time.Now().UTC()
		if request.UpdatedAt.Before(request.CreatedAt) {
			request.UpdatedAt = request.CreatedAt
		}
	})
	return nil
}

func (s *Store) StartRecovery(_ context.Context, requestID, tokenHash string, tokenExpiresAt, now time.Time) error {
	s.updateDeletion(requestID, func(request *account.DeletionRequest) {
		request.Token, request.TokenExpiresAt = tokenHash, tokenExpiresAt
		request.LifecycleState, request.UpdatedAt = "recovery_confirmation_pending", now
	})
	return nil
}

func (st state) pendingRecovery(email string, now time.Time) (account.DeletionRequest, bool) {
	for _, request := range st.deletions {
		if (request.LifecycleState == "scheduled" || request.LifecycleState == "recovery_confirmation_pending") &&
			request.ScheduledAt != nil && request.ScheduledAt.After(now) &&
			request.NotificationEmail != nil && strings.ToLower(*request.NotificationEmail) == email {
			return request, true
		}
	}
	return account.DeletionRequest{}, false
}

func (s *Store) PendingRecoveryIdentity(_ context.Context, email string, now time.Time) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found := s.state.pendingRecovery(email, now)
	return request.IdentityID, found, nil
}

func (s *Store) LockPendingRecovery(_ context.Context, email string, now time.Time) (account.DeletionRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found := s.state.pendingRecovery(email, now)
	return request, found, nil
}

func (s *Store) DeletionRequestPair(_ context.Context, requestID string) (string, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found := s.state.deletions[requestID]
	return request.MemberID, request.IdentityID, found, nil
}

func (s *Store) LockDueDeletionRequest(_ context.Context, requestID, memberID, identityID string, now time.Time) (account.DeletionRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found := s.state.deletions[requestID]
	if !found || request.MemberID != memberID || request.IdentityID != identityID ||
		(request.LifecycleState != "scheduled" && request.LifecycleState != "recovery_confirmation_pending") ||
		request.ScheduledAt == nil || request.ScheduledAt.After(now) {
		return account.DeletionRequest{}, false, nil
	}
	return request, true, nil
}

func (s *Store) DeletionRequestInStates(_ context.Context, requestID, identityID string, states []string) (account.DeletionRequest, bool, error) {
	if err := s.enter("DeletionRequestInStates"); err != nil {
		return account.DeletionRequest{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	request, found := s.state.deletions[requestID]
	if !found || request.IdentityID != identityID || !slices.Contains(states, request.LifecycleState) {
		return account.DeletionRequest{}, false, nil
	}
	return request, true, nil
}

func (s *Store) HasPendingDeletion(_ context.Context, identityID string) (bool, error) {
	if err := s.enter("HasPendingDeletion"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.state.deletions {
		if request.IdentityID == identityID && (request.LifecycleState == "scheduled" || request.LifecycleState == "recovery_confirmation_pending") {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) drift(match func(account.DeletionRequest, Identity) bool, limit int) []account.DeletionRequest {
	var result []account.DeletionRequest
	for _, request := range s.state.deletions {
		identity, found := s.state.identities[request.IdentityID]
		if found && match(request, identity) {
			result = append(result, request)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].UpdatedAt.Before(result[j].UpdatedAt)
		}
		return result[i].ID < result[j].ID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func (s *Store) ScheduledIdentityDrift(_ context.Context, limit int) ([]account.DeletionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drift(func(request account.DeletionRequest, identity Identity) bool {
		return (request.LifecycleState == "scheduled" || request.LifecycleState == "recovery_confirmation_pending") &&
			(identity.State == auth.KratosStateActive || len(identity.LiveSessions) != 0)
	}, limit), nil
}

func (s *Store) TerminalIdentityDrift(_ context.Context, limit int) ([]account.DeletionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drift(func(request account.DeletionRequest, identity Identity) bool {
		return (request.LifecycleState == "cancelled" || request.LifecycleState == "recovered") &&
			identity.State == auth.KratosStateInactive && request.UpdatedAt.After(identity.UpdatedAt)
	}, limit), nil
}

func (s *Store) LockAdminFence(context.Context) error { return s.enter("LockAdminFence") }

func (s *Store) ActiveAdminCount(_ context.Context, identityIDs []string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int64
	for memberID, member := range s.state.members {
		if !slices.Contains(identityIDs, member.IdentityID) || member.Deleted || !member.Onboarded {
			continue
		}
		identity, found := s.state.identities[member.IdentityID]
		if found && activeIdentity(identity) && !s.state.pendingDeletion(memberID, member.IdentityID) {
			count++
		}
	}
	return count, nil
}

func (s *Store) LockEmailChangeRequest(_ context.Context, identityID string) (*account.EmailChangeRequest, error) {
	if err := s.enter("LockEmailChangeRequest"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.state.emailChanges {
		if request.IdentityID == identityID {
			return &request, nil
		}
	}
	return nil, nil
}

// InsertEmailChangeRequest implements account.Store: one active request per
// Identity and one per Member.
func (s *Store) InsertEmailChangeRequest(_ context.Context, request account.EmailChangeRequest) error {
	if err := s.enter("InsertEmailChangeRequest"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.state.emailChanges {
		if existing.IdentityID == request.IdentityID || existing.MemberID == request.MemberID {
			return account.ErrEmailChangeRequestExists
		}
	}
	s.state.emailChanges[request.ID] = request
	return nil
}

func (s *Store) DeleteEmailChangeRequest(_ context.Context, requestID string) error {
	if err := s.enter("DeleteEmailChangeRequest"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.state.emailChanges[requestID]; !found {
		return account.ErrEmailChangeRequestNotFound
	}
	delete(s.state.emailChanges, requestID)
	return nil
}

func (s *Store) EmailChangeRequest(_ context.Context, requestID string) (*account.EmailChangeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request, found := s.state.emailChanges[requestID]; found {
		return &request, nil
	}
	return nil, nil
}

func (s *Store) findEmailChange(match func(account.EmailChangeRequest) bool) *account.EmailChangeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.state.emailChanges {
		if match(request) {
			return &request
		}
	}
	return nil
}

func (s *Store) EmailChangeRequestFor(_ context.Context, identityID, previousEmail, requestedEmail string) (*account.EmailChangeRequest, error) {
	return s.findEmailChange(func(request account.EmailChangeRequest) bool {
		return request.IdentityID == identityID && request.PreviousEmailAddress == previousEmail && request.RequestedEmailAddress == requestedEmail
	}), nil
}

func (s *Store) EmailChangeRequestTo(_ context.Context, identityID, recipient string) (*account.EmailChangeRequest, error) {
	return s.findEmailChange(func(request account.EmailChangeRequest) bool {
		return request.IdentityID == identityID && request.RequestedEmailAddress == recipient
	}), nil
}

func (s *Store) sortedEmailChanges() []account.EmailChangeRequest {
	requests := make([]account.EmailChangeRequest, 0, len(s.state.emailChanges))
	for _, request := range s.state.emailChanges {
		requests = append(requests, request)
	}
	sort.Slice(requests, func(i, j int) bool { return emailChangeBefore(requests[i], requests[j]) })
	return requests
}

func emailChangeBefore(left, right account.EmailChangeRequest) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return left.ID < right.ID
}

func (s *Store) LatestEmailChangeRequest(context.Context) (*account.EmailChangeRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := s.sortedEmailChanges()
	if len(requests) == 0 {
		return nil, nil
	}
	return &requests[len(requests)-1], nil
}

func (s *Store) EmailChangeRequests(_ context.Context, upper account.EmailChangeRequest, after *account.EmailChangeRequest, limit int) ([]account.EmailChangeRequest, error) {
	if err := s.enter("EmailChangeRequests"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var page []account.EmailChangeRequest
	for _, request := range s.sortedEmailChanges() {
		if emailChangeBefore(upper, request) || (after != nil && !emailChangeBefore(*after, request)) {
			continue
		}
		page = append(page, request)
		if len(page) == limit {
			break
		}
	}
	return page, nil
}

func (s *Store) MemberPrimaryEmail(_ context.Context, memberID, identityID string) (string, error) {
	if err := s.enter("MemberPrimaryEmail"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, _, linked := s.state.linked(memberID)
	if !linked || member.Deleted || member.IdentityID != identityID || strings.TrimSpace(member.PrimaryEmail) == "" {
		return "", account.ErrMemberLinkNotFound
	}
	return strings.TrimSpace(member.PrimaryEmail), nil
}

func (s *Store) SyncMemberEmails(_ context.Context, memberID, identityID, primaryEmail string, availableEmails []string) error {
	if err := s.enter("SyncMemberEmails"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, _, linked := s.state.linked(memberID)
	if !linked || member.Deleted || member.IdentityID != identityID {
		return account.ErrMemberLinkNotFound
	}
	if !slices.Contains(availableEmails, primaryEmail) {
		return errors.New("member primary email must be an available email")
	}
	member.PrimaryEmail, member.AvailableEmails = primaryEmail, slices.Clone(availableEmails)
	s.state.members[memberID] = member
	return nil
}

func (s *Store) MutateNewsletter(_ context.Context, identityID string, subscribed bool) (bool, error) {
	if err := s.enter("MutateNewsletter"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.state.newsletter[identityID]
	if subscribed {
		if !found {
			s.state.newsletter[identityID] = time.Now().UTC()
		}
		return !found, nil
	}
	delete(s.state.newsletter, identityID)
	return found, nil
}

func (s *Store) NewsletterState(_ context.Context, identityID string) (account.NewsletterState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, found := s.state.newsletter[identityID]; found {
		return account.NewsletterState{Subscribed: true, SubscribedAt: &at}, nil
	}
	return account.NewsletterState{}, nil
}

func (s *Store) DeleteAccountIdentityAnchor(_ context.Context, identityID string) error {
	if err := s.enter("DeleteAccountIdentityAnchor"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.state.anchors, identityID)
	return nil
}

func (s *Store) EnqueueUserDeleteIdentity(_ context.Context, command account.DeleteIdentityCommand) error {
	if err := s.enter("EnqueueUserDeleteIdentity"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.identityCmds = append(s.state.identityCmds, command)
	return nil
}

func (s *Store) EnqueueUserDeleteAvatar(_ context.Context, command account.DeleteAvatarCommand) error {
	if err := s.enter("EnqueueUserDeleteAvatar"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.avatarCmds = append(s.state.avatarCmds, command)
	return nil
}

func (s *Store) AppendRequestAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendRequest(ctx, nil, appender, action, build)
}

func (s *Store) AppendSystemAudit(ctx context.Context, appender domainaudit.Appender, action sharedtelemetry.AuditAction, build domainaudit.Builder) error {
	return domainaudit.AppendSystem(ctx, nil, appender, sharedtelemetry.ServiceBackend, action, build)
}

func (s *Store) AppendAuditRecord(ctx context.Context, appender domainaudit.Appender, record sharedtelemetry.AuditRecord) error {
	return appender.AppendDomainAuditInTransaction(ctx, nil, record)
}
