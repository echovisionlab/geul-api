package membertest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Fixture is one Store implementation under the contract and the rows of
// other domains its cases need.
type Fixture struct {
	mutationtest.Harness[member.Store]
	// SeedLinked stores an active Identity and a Member linked to it both
	// ways, onboarded as asked, and returns the Member and Identity IDs.
	SeedLinked func(t *testing.T, nickname string, onboarded bool) (memberID, identityID string)
	// SeedSegmentTag makes an audience segment use the tag.
	SeedSegmentTag func(t *testing.T, tagID string)
}

// FakeFixture is the Fixture of the in-memory store.
func FakeFixture(*testing.T) Fixture {
	store, _ := New()
	return Fixture{
		Harness: mutationtest.FakeHarness[member.Store](store),
		SeedLinked: func(_ *testing.T, nickname string, onboarded bool) (string, string) {
			memberID, identityID := uuid.NewString(), uuid.NewString()
			value := store.SeedLinked(memberID, identityID, nickname)
			value.Onboarded = onboarded
			store.SeedMember(value)
			return memberID, identityID
		},
		SeedSegmentTag: func(_ *testing.T, tagID string) { store.SeedSegmentTag(tagID) },
	}
}

// RunStoreContract runs every Store contract case against a fresh Fixture.
func RunStoreContract(t *testing.T, open func(t *testing.T) Fixture) {
	t.Helper()
	var fixtures sync.Map
	cases := make(map[string]func(*testing.T, mutationtest.Harness[member.Store]), len(storeContract))
	for name, run := range storeContract {
		cases[name] = func(t *testing.T, _ mutationtest.Harness[member.Store]) {
			fixture, _ := fixtures.Load(t)
			run(t, fixture.(Fixture))
		}
	}
	mutationtest.RunContract(t, func(t *testing.T) mutationtest.Harness[member.Store] {
		fixture := open(t)
		fixtures.Store(t, fixture)
		return fixture.Harness
	}, cases)
}

var errRolledBack = errors.New("roll the transaction back")

func transact(t *testing.T, f Fixture, work func(ctx context.Context, store member.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, f.Harness, work)
}

func nickname(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }

var storeContract = map[string]func(t *testing.T, f Fixture){
	"a nickname another Member holds answers ErrNicknameTaken": func(t *testing.T, f Fixture) {
		taken := nickname("taken")
		f.SeedLinked(t, taken, true)
		memberID, _ := f.SeedLinked(t, nickname("other"), true)
		err := transact(t, f, func(ctx context.Context, store member.Store) error {
			return store.UpdateProfile(ctx, memberID, member.ProfileUpdate{Nickname: &taken, UpdatedAt: time.Now().UTC()})
		})
		require.ErrorIs(t, err, member.ErrNicknameTaken)
	},
	"a profile update writes only the set fields": func(t *testing.T, f Fixture) {
		memberID, identityID := f.SeedLinked(t, nickname("profile"), true)
		bio, renamed := "Biography", nickname("renamed")
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			return store.UpdateProfile(ctx, memberID, member.ProfileUpdate{
				Nickname: &renamed, Bio: member.Optional{Set: true, Value: &bio},
				SocialLinks: map[string]string{"site": "https://example.test"}, UpdatedAt: time.Now().UTC(),
			})
		}))
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			current, err := store.LinkedMember(ctx, memberID, identityID)
			require.NoError(t, err)
			require.Equal(t, renamed, current.Nickname)
			require.Equal(t, &bio, current.Bio)
			require.Nil(t, current.Website)
			require.Equal(t, map[string]string{"site": "https://example.test"}, current.SocialLinks)
			return store.UpdateProfile(ctx, memberID, member.ProfileUpdate{Bio: member.Optional{Set: true}, UpdatedAt: time.Now().UTC()})
		}))
		require.NoError(t, mutationtest.Read(t, f.Harness, func(ctx context.Context, store member.Store) error {
			current, err := store.OwnMember(ctx, memberID)
			require.NoError(t, err)
			require.Nil(t, current.Bio)
			require.Equal(t, renamed, current.Nickname)
			return nil
		}))
	},
	"onboarding completes once and keeps the nickname unique": func(t *testing.T, f Fixture) {
		taken := nickname("claimed")
		f.SeedLinked(t, taken, true)
		memberID, identityID := f.SeedLinked(t, nickname("pending"), false)
		err := transact(t, f, func(ctx context.Context, store member.Store) error {
			_, err := store.CompleteOnboarding(ctx, memberID, identityID, taken, time.Now().UTC())
			return err
		})
		require.ErrorIs(t, err, member.ErrNicknameTaken)
		chosen := nickname("chosen")
		for _, want := range []bool{true, false} {
			require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
				transitioned, err := store.CompleteOnboarding(ctx, memberID, identityID, chosen, time.Now().UTC())
				require.Equal(t, want, transitioned)
				return err
			}))
		}
		require.NoError(t, mutationtest.Read(t, f.Harness, func(ctx context.Context, store member.Store) error {
			current, err := store.MemberByID(ctx, memberID)
			require.True(t, current.Onboarded)
			require.Equal(t, chosen, current.Nickname)
			return err
		}))
	},
	"a nickname claim ignores the Member itself and deleted Members": func(t *testing.T, f Fixture) {
		held := nickname("held")
		memberID, _ := f.SeedLinked(t, held, true)
		require.NoError(t, mutationtest.Read(t, f.Harness, func(ctx context.Context, store member.Store) error {
			claimed, err := store.NicknameClaimed(ctx, held, memberID)
			require.False(t, claimed)
			require.NoError(t, err)
			claimed, err = store.NicknameClaimed(ctx, held, uuid.NewString())
			require.True(t, claimed)
			return err
		}))
	},
	"targets require a live linked Identity and onboarding when asked": func(t *testing.T, f Fixture) {
		onboarded, identityID := f.SeedLinked(t, nickname("target"), true)
		pending, _ := f.SeedLinked(t, nickname("pending"), false)
		require.NoError(t, mutationtest.Read(t, f.Harness, func(ctx context.Context, store member.Store) error {
			target, err := store.CurrentTarget(ctx, onboarded)
			require.NoError(t, err)
			require.Equal(t, member.Target{MemberID: onboarded, IdentityID: identityID, Onboarded: true}, target)
			_, err = store.CurrentTarget(ctx, pending)
			require.ErrorIs(t, err, member.ErrIneligible)
			_, err = store.CurrentLinkedTarget(ctx, pending, false)
			require.NoError(t, err)
			_, err = store.CurrentLinkedTarget(ctx, uuid.NewString(), false)
			require.ErrorIs(t, err, member.ErrIneligible)
			return nil
		}))
	},
	"a duplicate tag name answers ErrTagNameTaken": func(t *testing.T, f Fixture) {
		name := nickname("tag")
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			_, err := store.InsertTag(ctx, name)
			return err
		}))
		err := transact(t, f, func(ctx context.Context, store member.Store) error {
			_, err := store.InsertTag(ctx, name)
			return err
		})
		require.ErrorIs(t, err, member.ErrTagNameTaken)
	},
	"tag assignments are replaced, counted and deleted with their tag": func(t *testing.T, f Fixture) {
		memberID, _ := f.SeedLinked(t, nickname("tagged"), true)
		var first, second member.Tag
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			var err error
			if first, err = store.InsertTag(ctx, nickname("first")); err != nil {
				return err
			}
			if second, err = store.InsertTag(ctx, nickname("second")); err != nil {
				return err
			}
			count, err := store.CountTags(ctx, []string{first.ID, second.ID, uuid.NewString()})
			require.Equal(t, int64(2), count)
			if err != nil {
				return err
			}
			return store.ReplaceMemberTags(ctx, memberID, []string{first.ID, second.ID})
		}))
		f.SeedSegmentTag(t, first.ID)
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			referenced, err := store.TagReferencedBySegment(ctx, first.ID)
			require.True(t, referenced)
			require.NoError(t, err)
			referenced, err = store.TagReferencedBySegment(ctx, second.ID)
			require.False(t, referenced)
			require.NoError(t, err)
			return store.DeleteTag(ctx, second.ID)
		}))
		require.NoError(t, mutationtest.Read(t, f.Harness, func(ctx context.Context, store member.Store) error {
			tags, err := store.TagIDs(ctx, []string{memberID})
			require.Equal(t, map[string][]string{memberID: {first.ID}}, tags)
			return err
		}))
		err := transact(t, f, func(ctx context.Context, store member.Store) error {
			_, err := store.LockTag(ctx, second.ID)
			return err
		})
		require.ErrorIs(t, err, member.ErrTagNotFound)
	},
	"a locked tag makes a second locker wait for the first commit": func(t *testing.T, f Fixture) {
		var tag member.Tag
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			var err error
			tag, err = store.InsertTag(ctx, nickname("locked"))
			return err
		}))
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store member.Store) error {
				_, err := store.LockTag(ctx, tag.ID)
				return err
			},
			func(ctx context.Context, store member.Store) error { return store.DeleteTag(ctx, tag.ID) },
			func(ctx context.Context, store member.Store) error {
				_, err := store.LockTag(ctx, tag.ID)
				return err
			},
		)
		require.ErrorIs(t, err, member.ErrTagNotFound)
	},
	"a failed transaction leaves no tag behind": func(t *testing.T, f Fixture) {
		name := nickname("rolled")
		err := transact(t, f, func(ctx context.Context, store member.Store) error {
			if _, err := store.InsertTag(ctx, name); err != nil {
				return err
			}
			return errRolledBack
		})
		require.ErrorIs(t, err, errRolledBack)
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			_, err := store.InsertTag(ctx, name)
			return err
		}))
	},
	"a newsletter opt-in keeps its first time and an opt-out reports a change once": func(t *testing.T, f Fixture) {
		_, identityID := f.SeedLinked(t, nickname("newsletter"), true)
		first := time.Date(2026, 4, 5, 6, 7, 8, 123456000, time.UTC)
		for _, subscribedAt := range []time.Time{first, first.Add(time.Hour)} {
			require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
				changed, err := store.SubscribeNewsletter(ctx, identityID, subscribedAt)
				require.Equal(t, subscribedAt.Equal(first), changed)
				return err
			}))
		}
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			subscribedAt, err := store.NewsletterSubscription(ctx, identityID)
			require.NoError(t, err)
			require.True(t, first.Equal(*subscribedAt))
			for _, want := range []bool{true, false} {
				changed, err := store.UnsubscribeNewsletter(ctx, identityID)
				require.NoError(t, err)
				require.Equal(t, want, changed)
			}
			subscribedAt, err = store.NewsletterSubscription(ctx, identityID)
			require.Nil(t, subscribedAt)
			return err
		}))
	},
	"the email projection of a linked pair is locked and replaced": func(t *testing.T, f Fixture) {
		memberID, identityID := f.SeedLinked(t, nickname("emails"), true)
		primary := "primary-" + uuid.NewString()[:8] + "@example.test"
		err := mutationtest.RequireSecondWaits(t, f.Harness,
			func(ctx context.Context, store member.Store) error {
				_, err := store.LockLinkedMember(ctx, memberID, identityID)
				return err
			},
			func(ctx context.Context, store member.Store) error {
				return store.UpdateEmailProjection(ctx, memberID, identityID, primary, []string{primary})
			},
			func(ctx context.Context, store member.Store) error {
				if _, err := store.LockLinkedMember(ctx, memberID, identityID); err != nil {
					return err
				}
				email, err := store.PrimaryEmail(ctx, memberID, identityID)
				require.Equal(t, primary, email)
				return err
			},
		)
		require.NoError(t, err)
		// The row lock needs a writable transaction.
		require.NoError(t, transact(t, f, func(ctx context.Context, store member.Store) error {
			_, err := store.PrimaryEmail(ctx, memberID, uuid.NewString())
			require.ErrorIs(t, err, member.ErrMemberNotFound)
			_, err = store.LockLinkedMember(ctx, uuid.NewString(), identityID)
			require.ErrorIs(t, err, member.ErrMemberNotFound)
			return nil
		}))
	},
}
