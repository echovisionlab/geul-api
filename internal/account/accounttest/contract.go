package accounttest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Fixture is one Store implementation under the contract and the Kratos and
// Member rows its cases need.
type Fixture struct {
	mutationtest.Harness[account.Store]
	// SeedPair stores an active Identity, its account anchor and a Member
	// linked to it both ways whose only email is the Identity's, onboarded
	// as asked.
	SeedPair func(t *testing.T, onboarded bool) (memberID, identityID, email string)
	// SeedSession stores a live session of an Identity.
	SeedSession func(t *testing.T, identityID string, authenticatedAt time.Time) string
	// SetIdentityState changes an Identity's state.
	SetIdentityState func(t *testing.T, identityID, state string)
}

// FakeFixture is the Fixture of the in-memory store.
func FakeFixture(*testing.T) Fixture {
	store, _ := New()
	return Fixture{
		Harness: mutationtest.FakeHarness[account.Store](store),
		SeedPair: func(_ *testing.T, onboarded bool) (string, string, string) {
			memberID, identityID := uuid.NewString(), uuid.NewString()
			email := identityID + "@example.test"
			store.SeedPair(memberID, identityID, email)
			store.UpdateMember(memberID, func(member *Member) { member.Onboarded = onboarded })
			return memberID, identityID, email
		},
		SeedSession: func(_ *testing.T, identityID string, authenticatedAt time.Time) string {
			sessionID := uuid.NewString()
			store.UpdateIdentity(identityID, func(identity *Identity) {
				identity.LiveSessions = append(identity.LiveSessions, account.Session{
					ID: sessionID, Active: true, AuthenticatedAt: authenticatedAt, ExpiresAt: authenticatedAt.Add(time.Hour),
				})
			})
			return sessionID
		},
		SetIdentityState: func(_ *testing.T, identityID, state string) {
			store.UpdateIdentity(identityID, func(identity *Identity) { identity.State = state })
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Fixture.
// The Member steps (PrepareMemberTombstone and the rest), the email
// projection and the newsletter delegate to Member's own ports, whose
// contract is Member's.
func RunStoreContract(t *testing.T, open func(t *testing.T) Fixture) {
	t.Helper()
	var fixtures sync.Map
	cases := make(map[string]func(*testing.T, mutationtest.Harness[account.Store]), len(storeContract))
	for name, run := range storeContract {
		cases[name] = func(t *testing.T, _ mutationtest.Harness[account.Store]) {
			fixture, _ := fixtures.Load(t)
			run(t, fixture.(Fixture))
		}
	}
	mutationtest.RunContract(t, func(t *testing.T) mutationtest.Harness[account.Store] {
		fixture := open(t)
		fixtures.Store(t, fixture)
		return fixture.Harness
	}, cases)
}

func transact(t *testing.T, f Fixture, work func(ctx context.Context, store account.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, f.Harness, work))
}

func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func deletionRequest(memberID, identityID, state string, at time.Time) account.DeletionRequest {
	return account.DeletionRequest{
		ID: uuid.NewString(), MemberID: memberID, IdentityID: identityID, Token: "token-" + uuid.NewString(),
		TokenExpiresAt: at.Add(time.Hour), LifecycleState: state, CreatedAt: at, UpdatedAt: at,
	}
}

func emailChange(memberID, identityID, previous string, at time.Time) account.EmailChangeRequest {
	return account.EmailChangeRequest{
		ID: uuid.NewString(), MemberID: memberID, IdentityID: identityID,
		PreviousEmailAddress: previous, RequestedEmailAddress: "next-" + uuid.NewString()[:8] + "@example.test", CreatedAt: at,
	}
}

var storeContract = map[string]func(t *testing.T, f Fixture){
	"a linked pair resolves its target and an unknown Member answers ErrIneligible": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, false)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			target, err := store.LinkedMemberForMember(ctx, memberID, false)
			require.NoError(t, err)
			require.Equal(t, account.Target{MemberID: memberID, IdentityID: identityID, Onboarded: false}, target)
			_, err = store.LinkedMemberForMember(ctx, memberID, true)
			require.ErrorIs(t, err, account.ErrIneligible)
			_, err = store.LinkedMemberForMember(ctx, uuid.NewString(), false)
			require.ErrorIs(t, err, account.ErrIneligible)
			return nil
		})
	},
	"an active pair validates and resolves, and a crossed or inactive pair answers ErrMemberLinkNotFound": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, true)
		otherMemberID, otherIdentityID, _ := f.SeedPair(t, true)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.NoError(t, store.ValidateActivePair(ctx, memberID, identityID))
			require.ErrorIs(t, store.ValidateActivePair(ctx, memberID, otherIdentityID), account.ErrMemberLinkNotFound)
			activeMemberID, err := store.ActiveMemberIDForIdentity(ctx, identityID)
			require.NoError(t, err)
			require.Equal(t, memberID, activeMemberID)
			target, err := store.ActiveOnboardedMemberForIdentity(ctx, otherIdentityID)
			require.NoError(t, err)
			require.Equal(t, account.Target{MemberID: otherMemberID, IdentityID: otherIdentityID, Onboarded: true}, target)
			return nil
		})
		f.SetIdentityState(t, identityID, auth.KratosStateInactive)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			_, err := store.ActiveMemberIDForIdentity(ctx, identityID)
			require.ErrorIs(t, err, account.ErrMemberLinkNotFound)
			require.ErrorIs(t, store.LockActivePairForCredentialMutation(ctx, memberID, identityID), account.ErrMemberLinkNotFound)
			return nil
		})
	},
	"the credential mutation lock requires an onboarded pair": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, false)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.ErrorIs(t, store.LockActivePairForCredentialMutation(ctx, memberID, identityID), account.ErrMemberLinkNotFound)
			_, err := store.ActiveOnboardedMemberForIdentity(ctx, identityID)
			require.ErrorIs(t, err, account.ErrMemberLinkNotFound)
			return nil
		})
	},
	"an Identity's existence, locked state and anchor are read and removed": func(t *testing.T, f Fixture) {
		_, identityID, _ := f.SeedPair(t, true)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.NoError(t, store.LockIdentity(ctx, identityID))
			state, err := store.LockedIdentityState(ctx, identityID)
			require.NoError(t, err)
			require.Equal(t, auth.KratosStateActive, state)
			exists, err := store.IdentityExists(ctx, identityID)
			require.NoError(t, err)
			require.True(t, exists)
			state, err = store.LockedIdentityState(ctx, uuid.NewString())
			require.NoError(t, err)
			require.Empty(t, state)
			exists, err = store.IdentityExists(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, exists)
			return store.DeleteAccountIdentityAnchor(ctx, identityID)
		})
	},
	"a deletion request is found by token, and an expired token or another state answers ErrDeletionRequestNotFound": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, true)
		at := now()
		request := deletionRequest(memberID, identityID, "confirmation_pending", at)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.NoError(t, store.InsertDeletionRequest(ctx, request))
			owner, err := store.DeletionRequestIdentityByToken(ctx, request.Token)
			require.NoError(t, err)
			require.Equal(t, identityID, owner)
			_, err = store.DeletionRequestIdentityByToken(ctx, "token-unknown")
			require.ErrorIs(t, err, account.ErrDeletionRequestNotFound)
			locked, err := store.LockDeletionRequestByToken(ctx, request.Token, at, []string{"confirmation_pending"})
			require.NoError(t, err)
			require.Equal(t, request.ID, locked.ID)
			require.Equal(t, memberID, locked.MemberID)
			_, err = store.LockDeletionRequestByToken(ctx, request.Token, at.Add(2*time.Hour), []string{"confirmation_pending"})
			require.ErrorIs(t, err, account.ErrDeletionRequestNotFound)
			_, err = store.LockDeletionRequestByToken(ctx, request.Token, at, []string{"scheduled"})
			require.ErrorIs(t, err, account.ErrDeletionRequestNotFound)
			pairMemberID, pairIdentityID, found, err := store.DeletionRequestPair(ctx, request.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, []string{memberID, identityID}, []string{pairMemberID, pairIdentityID})
			_, _, found, err = store.DeletionRequestPair(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"a scheduled deletion is pending in the summary and the administrator count until it ends": func(t *testing.T, f Fixture) {
		memberID, identityID, email := f.SeedPair(t, true)
		at := now()
		request := deletionRequest(memberID, identityID, "confirmation_pending", at)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.NoError(t, store.InsertDeletionRequest(ctx, request))
			pending, err := store.HasPendingDeletion(ctx, identityID)
			require.NoError(t, err)
			require.False(t, pending)
			count, err := store.ActiveAdminCount(ctx, []string{identityID})
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			return store.ScheduleDeletionRequest(ctx, request.ID, at, at.Add(24*time.Hour))
		})
		transact(t, f, func(ctx context.Context, store account.Store) error {
			pending, err := store.HasPendingDeletion(ctx, identityID)
			require.NoError(t, err)
			require.True(t, pending)
			count, err := store.ActiveAdminCount(ctx, []string{identityID})
			require.NoError(t, err)
			require.Zero(t, count)
			rows, err := store.AccountSummaries(ctx, []string{memberID})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.True(t, rows[0].PendingDelete)
			require.Equal(t, identityID, *rows[0].IdentityID)
			require.Equal(t, email, *rows[0].Email)
			require.Equal(t, auth.KratosStateActive, *rows[0].IdentityState)
			row, found, err := store.SessionSummary(ctx, memberID)
			require.NoError(t, err)
			require.True(t, found)
			require.True(t, row.PendingDelete)
			_, found, err = store.SessionSummary(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, found)
			current, found, err := store.LockPairDeletionRequest(ctx, memberID, identityID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "scheduled", current.LifecycleState)
			require.NotNil(t, current.ScheduledAt)
			require.True(t, current.ScheduledAt.Equal(at.Add(24*time.Hour)))
			return store.EndDeletionRequest(ctx, request.ID, "cancelled")
		})
		transact(t, f, func(ctx context.Context, store account.Store) error {
			pending, err := store.HasPendingDeletion(ctx, identityID)
			require.NoError(t, err)
			require.False(t, pending)
			current, found, err := store.DeletionRequestInStates(ctx, request.ID, identityID, []string{"cancelled"})
			require.NoError(t, err)
			require.True(t, found)
			require.Nil(t, current.ScheduledAt)
			require.False(t, current.UpdatedAt.Before(current.CreatedAt))
			_, found, err = store.DeletionRequestInStates(ctx, request.ID, identityID, []string{"scheduled"})
			require.NoError(t, err)
			require.False(t, found)
			require.NoError(t, store.DeletePairDeletionRequests(ctx, memberID, identityID))
			_, found, err = store.LockPairDeletionRequest(ctx, memberID, identityID)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"a pending recovery is found by its lower-case notification email until the deadline": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, true)
		at := now()
		scheduledAt := at.Add(24 * time.Hour)
		email := "Recover-" + uuid.NewString()[:8] + "@Example.test"
		request := deletionRequest(memberID, identityID, "scheduled", at)
		request.ScheduledAt, request.NotificationEmail = &scheduledAt, &email
		lower := strings.ToLower(email)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.NoError(t, store.InsertDeletionRequest(ctx, request))
			owner, found, err := store.PendingRecoveryIdentity(ctx, lower, at)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, identityID, owner)
			locked, found, err := store.LockPendingRecovery(ctx, lower, at)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, request.ID, locked.ID)
			_, found, err = store.PendingRecoveryIdentity(ctx, lower, scheduledAt)
			require.NoError(t, err)
			require.False(t, found)
			require.NoError(t, store.StartRecovery(ctx, request.ID, "token-recovery-"+request.ID, at.Add(time.Hour), at))
			current, err := store.LockDeletionRequestByToken(ctx, "token-recovery-"+request.ID, at, []string{"recovery_confirmation_pending"})
			require.NoError(t, err)
			require.Equal(t, request.ID, current.ID)
			return nil
		})
	},
	"only a due pending request of the exact pair is locked for dispatch": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, true)
		_, otherIdentityID, _ := f.SeedPair(t, true)
		at := now()
		dueAt := at.Add(-time.Minute)
		request := deletionRequest(memberID, identityID, "scheduled", at.Add(-time.Hour))
		request.ScheduledAt = &dueAt
		transact(t, f, func(ctx context.Context, store account.Store) error {
			require.NoError(t, store.InsertDeletionRequest(ctx, request))
			due, found, err := store.LockDueDeletionRequest(ctx, request.ID, memberID, identityID, at)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, request.ID, due.ID)
			_, found, err = store.LockDueDeletionRequest(ctx, request.ID, memberID, identityID, dueAt.Add(-time.Second))
			require.NoError(t, err)
			require.False(t, found)
			_, found, err = store.LockDueDeletionRequest(ctx, request.ID, memberID, otherIdentityID, at)
			require.NoError(t, err)
			require.False(t, found)
			require.NoError(t, store.DeleteDeletionRequest(ctx, request.ID))
			_, found, err = store.LockDueDeletionRequest(ctx, request.ID, memberID, identityID, at)
			require.NoError(t, err)
			require.False(t, found)
			return nil
		})
	},
	"identity drift lists a pending deletion of an active Identity and an ended one of an inactive Identity": func(t *testing.T, f Fixture) {
		scheduledMemberID, scheduledIdentityID, _ := f.SeedPair(t, true)
		quietMemberID, quietIdentityID, _ := f.SeedPair(t, true)
		endedMemberID, endedIdentityID, _ := f.SeedPair(t, true)
		f.SetIdentityState(t, quietIdentityID, auth.KratosStateInactive)
		f.SetIdentityState(t, endedIdentityID, auth.KratosStateInactive)
		at := now().Add(-time.Hour)
		scheduled := deletionRequest(scheduledMemberID, scheduledIdentityID, "scheduled", at)
		quiet := deletionRequest(quietMemberID, quietIdentityID, "scheduled", at)
		ended := deletionRequest(endedMemberID, endedIdentityID, "confirmation_pending", at)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			for _, request := range []account.DeletionRequest{scheduled, quiet, ended} {
				require.NoError(t, store.InsertDeletionRequest(ctx, request))
			}
			return store.EndDeletionRequest(ctx, ended.ID, "cancelled")
		})
		transact(t, f, func(ctx context.Context, store account.Store) error {
			drift, err := store.ScheduledIdentityDrift(ctx, 10)
			require.NoError(t, err)
			require.Equal(t, []string{scheduled.ID}, requestIDs(drift))
			drift, err = store.TerminalIdentityDrift(ctx, 10)
			require.NoError(t, err)
			require.Equal(t, []string{ended.ID}, requestIDs(drift))
			return nil
		})
	},
	"live sessions are listed newest first and the other sessions in ID order": func(t *testing.T, f Fixture) {
		_, identityID, _ := f.SeedPair(t, true)
		at := now()
		older := f.SeedSession(t, identityID, at.Add(-time.Minute))
		newer := f.SeedSession(t, identityID, at)
		third := f.SeedSession(t, identityID, at.Add(-2*time.Minute))
		transact(t, f, func(ctx context.Context, store account.Store) error {
			sessions, err := store.LiveSessions(ctx, identityID)
			require.NoError(t, err)
			ids := make([]string, len(sessions))
			for i, session := range sessions {
				ids[i] = session.ID
				require.True(t, session.Active)
			}
			require.Equal(t, []string{newer, older, third}, ids)
			owned, err := store.OwnsLiveSession(ctx, identityID, older)
			require.NoError(t, err)
			require.True(t, owned)
			owned, err = store.OwnsLiveSession(ctx, identityID, uuid.NewString())
			require.NoError(t, err)
			require.False(t, owned)
			others, err := store.OtherLiveSessionIDs(ctx, identityID, newer)
			require.NoError(t, err)
			expected := []string{older, third}
			if expected[0] > expected[1] {
				expected[0], expected[1] = expected[1], expected[0]
			}
			require.Equal(t, expected, others)
			return nil
		})
	},
	"an Identity holds one email change request, read back by transition and recipient": func(t *testing.T, f Fixture) {
		memberID, identityID, email := f.SeedPair(t, true)
		request := emailChange(memberID, identityID, email, now())
		transact(t, f, func(ctx context.Context, store account.Store) error {
			locked, err := store.LockEmailChangeRequest(ctx, identityID)
			require.NoError(t, err)
			require.Nil(t, locked)
			return store.InsertEmailChangeRequest(ctx, request)
		})
		err := mutationtest.Transact(t, f.Harness, func(ctx context.Context, store account.Store) error {
			second := emailChange(memberID, identityID, email, now())
			return store.InsertEmailChangeRequest(ctx, second)
		})
		require.ErrorIs(t, err, account.ErrEmailChangeRequestExists)
		transact(t, f, func(ctx context.Context, store account.Store) error {
			locked, err := store.LockEmailChangeRequest(ctx, identityID)
			require.NoError(t, err)
			require.NotNil(t, locked)
			require.Equal(t, request.ID, locked.ID)
			require.True(t, locked.CreatedAt.Equal(request.CreatedAt))
			read, err := store.EmailChangeRequest(ctx, request.ID)
			require.NoError(t, err)
			require.Equal(t, request.RequestedEmailAddress, read.RequestedEmailAddress)
			read, err = store.EmailChangeRequestFor(ctx, identityID, email, request.RequestedEmailAddress)
			require.NoError(t, err)
			require.NotNil(t, read)
			read, err = store.EmailChangeRequestFor(ctx, identityID, request.RequestedEmailAddress, email)
			require.NoError(t, err)
			require.Nil(t, read)
			read, err = store.EmailChangeRequestTo(ctx, identityID, request.RequestedEmailAddress)
			require.NoError(t, err)
			require.NotNil(t, read)
			read, err = store.EmailChangeRequestTo(ctx, identityID, email)
			require.NoError(t, err)
			require.Nil(t, read)
			require.NoError(t, store.DeleteEmailChangeRequest(ctx, request.ID))
			require.ErrorIs(t, store.DeleteEmailChangeRequest(ctx, request.ID), account.ErrEmailChangeRequestNotFound)
			read, err = store.EmailChangeRequest(ctx, request.ID)
			require.NoError(t, err)
			require.Nil(t, read)
			return nil
		})
	},
	"email change requests are paged oldest first under the upper bound": func(t *testing.T, f Fixture) {
		at := now().Add(-time.Hour)
		var requests []account.EmailChangeRequest
		for i := range 4 {
			memberID, identityID, email := f.SeedPair(t, true)
			requests = append(requests, emailChange(memberID, identityID, email, at.Add(time.Duration(i)*time.Minute)))
		}
		transact(t, f, func(ctx context.Context, store account.Store) error {
			latest, err := store.LatestEmailChangeRequest(ctx)
			require.NoError(t, err)
			require.Nil(t, latest)
			for _, request := range requests {
				require.NoError(t, store.InsertEmailChangeRequest(ctx, request))
			}
			return nil
		})
		transact(t, f, func(ctx context.Context, store account.Store) error {
			latest, err := store.LatestEmailChangeRequest(ctx)
			require.NoError(t, err)
			require.Equal(t, requests[3].ID, latest.ID)
			page, err := store.EmailChangeRequests(ctx, requests[2], nil, 2)
			require.NoError(t, err)
			require.Equal(t, []string{requests[0].ID, requests[1].ID}, emailChangeIDs(page))
			page, err = store.EmailChangeRequests(ctx, requests[2], &page[1], 2)
			require.NoError(t, err)
			require.Equal(t, []string{requests[2].ID}, emailChangeIDs(page))
			return nil
		})
	},
	"a locked deletion request makes a second locker wait for the first commit": func(t *testing.T, f Fixture) {
		memberID, identityID, _ := f.SeedPair(t, true)
		request := deletionRequest(memberID, identityID, "confirmation_pending", now())
		transact(t, f, func(ctx context.Context, store account.Store) error {
			return store.InsertDeletionRequest(ctx, request)
		})
		var seen account.DeletionRequest
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store account.Store) error {
				_, _, err := store.LockPairDeletionRequest(ctx, memberID, identityID)
				return err
			},
			func(ctx context.Context, store account.Store) error {
				return store.EndDeletionRequest(ctx, request.ID, "cancelled")
			},
			func(ctx context.Context, store account.Store) error {
				current, found, err := store.LockPairDeletionRequest(ctx, memberID, identityID)
				if err == nil && !found {
					err = errors.New("the request is gone")
				}
				seen = current
				return err
			})
		require.NoError(t, err)
		require.Equal(t, "cancelled", seen.LifecycleState)
	},
	"a locked email change request makes a second locker wait for the first commit": func(t *testing.T, f Fixture) {
		memberID, identityID, email := f.SeedPair(t, true)
		request := emailChange(memberID, identityID, email, now())
		transact(t, f, func(ctx context.Context, store account.Store) error {
			return store.InsertEmailChangeRequest(ctx, request)
		})
		var seen *account.EmailChangeRequest
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store account.Store) error {
				_, err := store.LockEmailChangeRequest(ctx, identityID)
				return err
			},
			func(ctx context.Context, store account.Store) error {
				return store.DeleteEmailChangeRequest(ctx, request.ID)
			},
			func(ctx context.Context, store account.Store) error {
				var err error
				seen, err = store.LockEmailChangeRequest(ctx, identityID)
				return err
			})
		require.NoError(t, err)
		require.Nil(t, seen)
	},
}

func requestIDs(requests []account.DeletionRequest) []string {
	ids := make([]string, len(requests))
	for i, request := range requests {
		ids[i] = request.ID
	}
	return ids
}

func emailChangeIDs(requests []account.EmailChangeRequest) []string {
	ids := make([]string, len(requests))
	for i, request := range requests {
		ids[i] = request.ID
	}
	return ids
}
