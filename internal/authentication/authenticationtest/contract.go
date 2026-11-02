package authenticationtest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Harness is one Store implementation the contract runs against, with the
// Members and account Identities it cannot store itself: their rows belong
// to Member and Kratos.
type Harness struct {
	mutationtest.Harness[authentication.Store]
	// SeedMember stores a Member linked to a new account Identity in the
	// given state and returns both IDs.
	SeedMember func(t *testing.T, state Member) (identityID, memberID string)
	// SeedIdentity stores an account Identity without a Member.
	SeedIdentity func(t *testing.T) string
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[authentication.Store](store),
		SeedMember: func(_ *testing.T, state Member) (string, string) {
			state.IdentityID = uuid.NewString()
			memberID := uuid.NewString()
			store.SeedMember(memberID, state)
			return state.IdentityID, memberID
		},
		SeedIdentity: func(*testing.T) string {
			identityID := uuid.NewString()
			store.SeedIdentity(identityID)
			return identityID
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel, as mutationtest.RunContract does.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

var errRollback = errors.New("roll the transaction back")

func transact(t *testing.T, h Harness, work func(ctx context.Context, store authentication.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func claim(t *testing.T, h Harness, identityID, memberID string) authentication.FirstAdminClaim {
	t.Helper()
	var result authentication.FirstAdminClaim
	require.NoError(t, transact(t, h, func(ctx context.Context, store authentication.Store) error {
		var err error
		result, err = store.ClaimFirstAdmin(ctx, identityID, memberID)
		return err
	}))
	return result
}

func usage(t *testing.T, h Harness, query authentication.IssuanceUsageQuery) authentication.IssuanceUsage {
	t.Helper()
	var result authentication.IssuanceUsage
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store authentication.Store) error {
		var err error
		result, err = store.IssuanceUsage(ctx, query)
		return err
	}))
	return result
}

func latest(t *testing.T, h Harness, recipientDigest string, now time.Time) (authentication.IssuedCode, bool) {
	t.Helper()
	var code authentication.IssuedCode
	var found bool
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store authentication.Store) error {
		var err error
		code, found, err = store.LatestIssuance(ctx, recipientDigest, now)
		return err
	}))
	return code, found
}

func insertIssuances(t *testing.T, h Harness, issuances ...authentication.Issuance) {
	t.Helper()
	require.NoError(t, transact(t, h, func(ctx context.Context, store authentication.Store) error {
		for _, issuance := range issuances {
			if err := store.InsertIssuance(ctx, issuance); err != nil {
				return err
			}
		}
		return nil
	}))
}

// digest is a value the ledger's digest shape check accepts.
func digest(symbol string) string { return strings.Repeat(symbol, 64) }

// issuance is a ledger row issued at issuedAt that lives an hour.
func issuance(purpose email.EventKey, recipient, subject, clientIP string, issuedAt time.Time) authentication.Issuance {
	return authentication.Issuance{
		Token: strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", ""), Purpose: purpose,
		RecipientDigest: recipient, SubjectDigest: subject, ClientIPDigest: clientIP,
		IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(time.Hour),
	}
}

// at is a time both stores keep to the microsecond.
func at(minutes int) time.Time {
	return time.Date(2026, time.July, 31, 10, 0, 0, 123456000, time.UTC).Add(time.Duration(minutes) * time.Minute)
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"the only onboarded linked Member claims the first admin once": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{Onboarded: true})

		require.Equal(t, authentication.FirstAdminClaim{Owner: true, ClaimedNow: true}, claim(t, h, identityID, memberID))
		require.Equal(t, authentication.FirstAdminClaim{Owner: true}, claim(t, h, identityID, memberID))
	},
	"another account Identity keeps the greenfield closed": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{Onboarded: true})
		h.SeedIdentity(t)

		require.Equal(t, authentication.FirstAdminClaim{}, claim(t, h, identityID, memberID))
	},
	"a Member that is not onboarded cannot claim the first admin": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{})

		require.Equal(t, authentication.FirstAdminClaim{}, claim(t, h, identityID, memberID))
	},
	"a banned Identity cannot claim the first admin": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{Onboarded: true, Banned: true})

		require.Equal(t, authentication.FirstAdminClaim{}, claim(t, h, identityID, memberID))
	},
	"a deleted Member cannot claim the first admin": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{Onboarded: true, Deleted: true})

		require.Equal(t, authentication.FirstAdminClaim{}, claim(t, h, identityID, memberID))
	},
	"a claim names only its own Identity as owner": func(t *testing.T, h Harness) {
		ownerIdentityID, ownerMemberID := h.SeedMember(t, Member{Onboarded: true})
		require.True(t, claim(t, h, ownerIdentityID, ownerMemberID).ClaimedNow)
		laterIdentityID, laterMemberID := h.SeedMember(t, Member{Onboarded: true})

		require.Equal(t, authentication.FirstAdminClaim{}, claim(t, h, laterIdentityID, laterMemberID))
		require.Equal(t, authentication.FirstAdminClaim{Owner: true}, claim(t, h, ownerIdentityID, ownerMemberID))
	},
	"a rolled-back claim leaves the first admin unclaimed": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{Onboarded: true})
		err := transact(t, h, func(ctx context.Context, store authentication.Store) error {
			if _, err := store.ClaimFirstAdmin(ctx, identityID, memberID); err != nil {
				return err
			}
			return errRollback
		})
		require.ErrorIs(t, err, errRollback)

		require.Equal(t, authentication.FirstAdminClaim{Owner: true, ClaimedNow: true}, claim(t, h, identityID, memberID))
	},
	"a claim makes a second claimant wait for the first commit": func(t *testing.T, h Harness) {
		identityID, memberID := h.SeedMember(t, Member{Onboarded: true})
		var first, second authentication.FirstAdminClaim

		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store authentication.Store) error {
				var err error
				first, err = store.ClaimFirstAdmin(ctx, identityID, memberID)
				return err
			},
			func(context.Context, authentication.Store) error { return nil },
			func(ctx context.Context, store authentication.Store) error {
				var err error
				second, err = store.ClaimFirstAdmin(ctx, identityID, memberID)
				return err
			})

		require.NoError(t, err)
		require.Equal(t, authentication.FirstAdminClaim{Owner: true, ClaimedNow: true}, first)
		require.Equal(t, authentication.FirstAdminClaim{Owner: true}, second)
	},
	"an issuance counts in the budgets that select it once it is in the window": func(t *testing.T, h Harness) {
		older := issuance(email.EventLoginCode, digest("a"), digest("b"), digest("c"), at(0))
		newer := issuance(email.EventVerificationCode, digest("d"), digest("e"), digest("c"), at(1))
		insertIssuances(t, h, older, newer)

		require.Equal(t, authentication.IssuanceUsage{Count: 2, First: at(0)},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.EveryIssuance, After: at(-1)}))
		require.Equal(t, authentication.IssuanceUsage{Count: 1, First: at(1)},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.EveryIssuance, After: at(0)}),
			"the window excludes an issuance at its opening instant")
		require.Equal(t, authentication.IssuanceUsage{Count: 2, First: at(0)},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.ClientIPIssuances, Digest: digest("c"), After: at(-1)}))
		require.Equal(t, authentication.IssuanceUsage{Count: 1, First: at(1)},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.SubjectIssuances, Digest: digest("e"), After: at(-1)}))
		require.Equal(t, authentication.IssuanceUsage{},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.SubjectIssuances, Digest: digest("c"), After: at(-1)}),
			"a subject budget does not read client IP digests")
	},
	"the latest live issuance to a recipient is its current code": func(t *testing.T, h Harness) {
		recipient := digest("a")
		expired := issuance(email.EventLoginCode, recipient, digest("b"), digest("c"), at(-120))
		older := issuance(email.EventLoginCode, recipient, digest("b"), digest("c"), at(0))
		newer := issuance(email.EventVerificationCode, recipient, digest("b"), digest("c"), at(1))
		other := issuance(email.EventLoginCode, digest("f"), digest("b"), digest("c"), at(2))
		insertIssuances(t, h, expired, older, newer, other)

		code, found := latest(t, h, recipient, at(3))
		require.True(t, found)
		require.Equal(t, authentication.IssuedCode{
			Token: newer.Token, Purpose: email.EventVerificationCode, RecipientDigest: recipient, IssuedAt: at(1),
		}, code)
		require.True(t, code.IssuedAt.Equal(newer.IssuedAt))
		_, found = latest(t, h, recipient, at(61))
		require.False(t, found, "an issuance expires at its expiry instant")
		_, found = latest(t, h, digest("9"), at(3))
		require.False(t, found)
	},
	"a deleted issuance leaves every budget and the current code": func(t *testing.T, h Harness) {
		deleted := issuance(email.EventLoginCode, digest("a"), digest("b"), digest("c"), at(0))
		insertIssuances(t, h, deleted)

		require.NoError(t, transact(t, h, func(ctx context.Context, store authentication.Store) error {
			if err := store.DeleteIssuance(ctx, deleted.Token); err != nil {
				return err
			}
			return store.DeleteIssuance(ctx, deleted.Token)
		}))

		require.Equal(t, authentication.IssuanceUsage{},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.EveryIssuance, After: at(-1)}))
		_, found := latest(t, h, digest("a"), at(1))
		require.False(t, found)
	},
	"a rolled-back issuance leaves no trace": func(t *testing.T, h Harness) {
		err := transact(t, h, func(ctx context.Context, store authentication.Store) error {
			if err := store.InsertIssuance(ctx, issuance(email.EventLoginCode, digest("a"), digest("b"), digest("c"), at(0))); err != nil {
				return err
			}
			return errRollback
		})
		require.ErrorIs(t, err, errRollback)

		require.Equal(t, authentication.IssuanceUsage{},
			usage(t, h, authentication.IssuanceUsageQuery{Scope: authentication.EveryIssuance, After: at(-1)}))
	},
	"the issuance lock makes a second reservation wait for the first commit": func(t *testing.T, h Harness) {
		var seen authentication.IssuanceUsage

		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store authentication.Store) error { return store.LockIssuances(ctx) },
			func(ctx context.Context, store authentication.Store) error {
				return store.InsertIssuance(ctx, issuance(email.EventLoginCode, digest("a"), digest("b"), digest("c"), at(0)))
			},
			func(ctx context.Context, store authentication.Store) error {
				if err := store.LockIssuances(ctx); err != nil {
					return err
				}
				var err error
				seen, err = store.IssuanceUsage(ctx, authentication.IssuanceUsageQuery{Scope: authentication.EveryIssuance, After: at(-1)})
				return err
			})

		require.NoError(t, err)
		require.Equal(t, authentication.IssuanceUsage{Count: 1, First: at(0)}, seen)
	},
}
