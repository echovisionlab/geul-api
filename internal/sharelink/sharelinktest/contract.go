package sharelinktest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
)

// Harness is one Store implementation the contract runs against, with the
// link targets it cannot store itself: their rows belong to other domains.
type Harness struct {
	mutationtest.Harness[sharelink.Store]
	// SeedTarget stores a target of entityType in the given state and
	// returns its ID.
	SeedTarget func(t *testing.T, entityType sharelink.EntityType, state sharelink.Target) string
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[sharelink.Store](store),
		SeedTarget: func(_ *testing.T, entityType sharelink.EntityType, state sharelink.Target) string {
			id := uuid.NewString()
			store.SeedTarget(entityType, id, state)
			return id
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

// ArgonHash is a stored password hash the share_link check accepts.
const ArgonHash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2g"

// now is a time both stores keep to the microsecond.
func now() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func linkOn(entityType sharelink.EntityType, entityID string, createdAt time.Time) sharelink.Link {
	return sharelink.Link{
		Token: uuid.NewString(), EntityType: entityType, EntityID: entityID,
		CreatedAt: createdAt, ExpiresAt: createdAt.Add(time.Hour),
	}
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store sharelink.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func insert(t *testing.T, h Harness, links ...sharelink.Link) []sharelink.Link {
	t.Helper()
	stored := make([]sharelink.Link, len(links))
	require.NoError(t, transact(t, h, func(ctx context.Context, store sharelink.Store) error {
		for i, link := range links {
			var err error
			if stored[i], err = store.InsertLink(ctx, link); err != nil {
				return err
			}
		}
		return nil
	}))
	return stored
}

func linkByToken(t *testing.T, h Harness, token string) (sharelink.Link, error) {
	t.Helper()
	var link sharelink.Link
	err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store sharelink.Store) (err error) {
		link, err = store.LinkByToken(ctx, token)
		return err
	})
	return link, err
}

func listLinks(t *testing.T, h Harness, entityType sharelink.EntityType, entityID string) []string {
	t.Helper()
	var links []sharelink.Link
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store sharelink.Store) (err error) {
		links, err = store.ListLinks(ctx, entityType, entityID)
		return err
	}))
	return tokens(links)
}

func tokens(links []sharelink.Link) []string {
	tokens := make([]string, len(links))
	for i, link := range links {
		tokens[i] = link.Token
	}
	return tokens
}

func requireSameLink(t *testing.T, want, got sharelink.Link) {
	t.Helper()
	require.Equal(t, want.ID, got.ID)
	require.Equal(t, want.Token, got.Token)
	require.Equal(t, want.EntityType, got.EntityType)
	require.Equal(t, want.EntityID, got.EntityID)
	require.Equal(t, want.Label, got.Label)
	require.Equal(t, want.PasswordHash, got.PasswordHash)
	require.True(t, want.CreatedAt.Equal(got.CreatedAt), "created at %v, want %v", got.CreatedAt, want.CreatedAt)
	require.True(t, want.ExpiresAt.Equal(got.ExpiresAt), "expires at %v, want %v", got.ExpiresAt, want.ExpiresAt)
}

func readTarget(t *testing.T, h Harness, entityType sharelink.EntityType, entityID string, lock bool) (sharelink.Target, error) {
	t.Helper()
	var target sharelink.Target
	err := transact(t, h, func(ctx context.Context, store sharelink.Store) (err error) {
		if lock {
			target, err = store.LockTarget(ctx, entityType, entityID)
		} else {
			target, err = store.Target(ctx, entityType, entityID)
		}
		return err
	})
	return target, err
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"an inserted link reads back by ID and token as stored": func(t *testing.T, h Harness) {
		label, hash := "press", ArgonHash
		link := linkOn(sharelink.EntityPost, uuid.NewString(), now())
		link.Label, link.PasswordHash = &label, &hash
		inserted := insert(t, h, link)[0]
		require.NotEmpty(t, inserted.ID)
		link.ID = inserted.ID
		requireSameLink(t, link, inserted)

		require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, store sharelink.Store) error {
			byID, err := store.Link(ctx, inserted.ID)
			if err != nil {
				return err
			}
			requireSameLink(t, link, byID)
			return nil
		}))
		byToken, err := linkByToken(t, h, link.Token)
		require.NoError(t, err)
		requireSameLink(t, link, byToken)
	},
	"a missing link is not found by ID, by token or on delete": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		err := mutationtest.Read(t, h.Harness, func(ctx context.Context, store sharelink.Store) error {
			_, err := store.Link(ctx, missing)
			return err
		})
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
		_, err = linkByToken(t, h, "missing-token")
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
		err = transact(t, h, func(ctx context.Context, store sharelink.Store) error {
			return store.DeleteLink(ctx, sharelink.Link{ID: missing, EntityType: sharelink.EntityPost, EntityID: uuid.NewString()})
		})
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
	},
	"a target's links list newest first and without other targets' links": func(t *testing.T, h Harness) {
		entityID, start := uuid.NewString(), now()
		oldest := linkOn(sharelink.EntityPage, entityID, start.Add(-2*time.Minute))
		newest := linkOn(sharelink.EntityPage, entityID, start)
		middle := linkOn(sharelink.EntityPage, entityID, start.Add(-time.Minute))
		insert(t, h, oldest, newest, middle,
			linkOn(sharelink.EntityPage, uuid.NewString(), start),
			linkOn(sharelink.EntityPost, entityID, start))

		require.Equal(t, []string{newest.Token, middle.Token, oldest.Token}, listLinks(t, h, sharelink.EntityPage, entityID))
		require.Empty(t, listLinks(t, h, sharelink.EntityWork, entityID))
	},
	"a live link needs its token, its target and an expiry after now": func(t *testing.T, h Harness) {
		entityID, start := uuid.NewString(), now()
		link := insert(t, h, linkOn(sharelink.EntityLabel, entityID, start))[0]
		live := func(token string, entityType sharelink.EntityType, entityID string, at time.Time) error {
			return mutationtest.Read(t, h.Harness, func(ctx context.Context, store sharelink.Store) error {
				found, err := store.LiveLink(ctx, token, entityType, entityID, at)
				if err == nil {
					require.Equal(t, link.ID, found.ID)
				}
				return err
			})
		}

		require.NoError(t, live(link.Token, sharelink.EntityLabel, entityID, start))
		require.ErrorIs(t, live(link.Token, sharelink.EntityArtist, entityID, start), sharelink.ErrLinkNotFound)
		require.ErrorIs(t, live(link.Token, sharelink.EntityLabel, uuid.NewString(), start), sharelink.ErrLinkNotFound)
		require.ErrorIs(t, live("other-token", sharelink.EntityLabel, entityID, start), sharelink.ErrLinkNotFound)
		require.ErrorIs(t, live(link.Token, sharelink.EntityLabel, entityID, link.ExpiresAt), sharelink.ErrLinkNotFound)
	},
	"a link is deleted only while it names its target": func(t *testing.T, h Harness) {
		link := insert(t, h, linkOn(sharelink.EntityRelease, uuid.NewString(), now()))[0]
		moved := link
		moved.EntityID = uuid.NewString()
		deleteLink := func(link sharelink.Link) error {
			return transact(t, h, func(ctx context.Context, store sharelink.Store) error { return store.DeleteLink(ctx, link) })
		}

		require.ErrorIs(t, deleteLink(moved), sharelink.ErrLinkNotFound)
		_, err := linkByToken(t, h, link.Token)
		require.NoError(t, err)
		require.NoError(t, deleteLink(link))
		_, err = linkByToken(t, h, link.Token)
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
		require.ErrorIs(t, deleteLink(link), sharelink.ErrLinkNotFound)
	},
	"target links are deleted for the listed kinds of that target only": func(t *testing.T, h Harness) {
		pageID, start := uuid.NewString(), now()
		legacy := linkOn("page", pageID, start)
		current := linkOn(sharelink.EntityPage, pageID, start)
		otherKind := linkOn(sharelink.EntityPost, pageID, start)
		otherPage := linkOn(sharelink.EntityPage, uuid.NewString(), start)
		insert(t, h, legacy, current, otherKind, otherPage)

		require.NoError(t, transact(t, h, func(ctx context.Context, store sharelink.Store) error {
			return store.DeleteTargetLinks(ctx, pageID, "page", sharelink.EntityPage)
		}))
		for token, kept := range map[string]bool{legacy.Token: false, current.Token: false, otherKind.Token: true, otherPage.Token: true} {
			_, err := linkByToken(t, h, token)
			if kept {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
			}
		}
	},
	"a password-free link is deleted by token while a protected one stays": func(t *testing.T, h Harness) {
		entityID, hash := uuid.NewString(), ArgonHash
		open := linkOn(sharelink.EntityPrivacy, entityID, now())
		protected := linkOn(sharelink.EntityPrivacy, entityID, now())
		protected.PasswordHash = &hash
		insert(t, h, open, protected)

		require.NoError(t, transact(t, h, func(ctx context.Context, store sharelink.Store) error {
			if err := store.DeletePasswordFreeLink(ctx, open.Token, sharelink.EntityPrivacy, entityID); err != nil {
				return err
			}
			return store.DeletePasswordFreeLink(ctx, protected.Token, sharelink.EntityPrivacy, entityID)
		}))
		_, err := linkByToken(t, h, open.Token)
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
		_, err = linkByToken(t, h, protected.Token)
		require.NoError(t, err)
	},
	"expired links are deleted and counted": func(t *testing.T, h Harness) {
		start := now()
		expired := linkOn(sharelink.EntityWork, uuid.NewString(), start.Add(-2*time.Hour))
		live := linkOn(sharelink.EntityWork, uuid.NewString(), start)
		insert(t, h, expired, live)

		var deleted int64
		require.NoError(t, transact(t, h, func(ctx context.Context, store sharelink.Store) (err error) {
			deleted, err = store.DeleteExpiredLinks(ctx, start)
			return err
		}))
		require.EqualValues(t, 1, deleted)
		_, err := linkByToken(t, h, expired.Token)
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
		_, err = linkByToken(t, h, live.Token)
		require.NoError(t, err)
	},
	"a second insert of a held token keeps the first link": func(t *testing.T, h Harness) {
		first := linkOn(sharelink.EntityTerms, uuid.NewString(), now())
		retry := first
		retry.EntityID, retry.ExpiresAt = uuid.NewString(), first.ExpiresAt.Add(time.Hour)
		for _, link := range []sharelink.Link{first, retry} {
			require.NoError(t, transact(t, h, func(ctx context.Context, store sharelink.Store) error {
				return store.InsertLinkUnlessTokenExists(ctx, link)
			}))
		}

		stored, err := linkByToken(t, h, first.Token)
		require.NoError(t, err)
		first.ID = stored.ID
		requireSameLink(t, first, stored)
	},
	"a failed transaction stores no link": func(t *testing.T, h Harness) {
		failed := errors.New("a later step failed")
		link := linkOn(sharelink.EntityArtist, uuid.NewString(), now())
		err := transact(t, h, func(ctx context.Context, store sharelink.Store) error {
			if _, err := store.InsertLink(ctx, link); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		_, err = linkByToken(t, h, link.Token)
		require.ErrorIs(t, err, sharelink.ErrLinkNotFound)
	},
	"a target reads its stored lifecycle, locked or not": func(t *testing.T, h Harness) {
		effective := now().Add(time.Hour)
		states := map[sharelink.EntityType][]sharelink.Target{
			sharelink.EntityPost:          {{}, {Archived: true}},
			sharelink.EntityWork:          {{}, {Archived: true}},
			sharelink.EntityPage:          {{}},
			sharelink.EntityRelease:       {{}},
			sharelink.EntityForm:          {{}},
			sharelink.EntityFormDashboard: {{}},
			sharelink.EntityLabel:         {{}},
			sharelink.EntityArtist:        {{}},
			sharelink.EntityPrivacy:       {{Version: 1}, {Scheduled: true, EffectiveFrom: &effective, Version: 2}, {Archived: true, Version: 3}},
			sharelink.EntityTerms:         {{Version: 4}, {Scheduled: true, EffectiveFrom: &effective, Version: 5}, {Archived: true, Version: 6}},
		}
		for entityType, seeded := range states {
			for _, state := range seeded {
				id := h.SeedTarget(t, entityType, state)
				for _, lock := range []bool{false, true} {
					target, err := readTarget(t, h, entityType, id, lock)
					require.NoError(t, err, entityType)
					require.Equal(t, state.Archived, target.Archived, entityType)
					require.Equal(t, state.Scheduled, target.Scheduled, entityType)
					require.Equal(t, state.Version, target.Version, entityType)
					if state.EffectiveFrom == nil {
						require.Nil(t, target.EffectiveFrom, entityType)
					} else {
						require.NotNil(t, target.EffectiveFrom, entityType)
						require.True(t, state.EffectiveFrom.Equal(*target.EffectiveFrom), entityType)
					}
				}
			}
			for _, lock := range []bool{false, true} {
				_, err := readTarget(t, h, entityType, uuid.NewString(), lock)
				require.ErrorIs(t, err, sharelink.ErrTargetNotFound, entityType)
			}
		}
	},
	"a form target serves its submission and dashboard links": func(t *testing.T, h Harness) {
		id := h.SeedTarget(t, sharelink.EntityForm, sharelink.Target{})
		_, err := readTarget(t, h, sharelink.EntityFormDashboard, id, true)
		require.NoError(t, err)
	},
	"a locked target makes a second link change wait for the first commit": func(t *testing.T, h Harness) {
		postID := h.SeedTarget(t, sharelink.EntityPost, sharelink.Target{})
		link := linkOn(sharelink.EntityPost, postID, now())
		var seen []sharelink.Link
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store sharelink.Store) error {
				_, err := store.LockTarget(ctx, sharelink.EntityPost, postID)
				return err
			},
			func(ctx context.Context, store sharelink.Store) error {
				_, err := store.InsertLink(ctx, link)
				return err
			},
			func(ctx context.Context, store sharelink.Store) (err error) {
				if _, err = store.LockTarget(ctx, sharelink.EntityPost, postID); err != nil {
					return err
				}
				seen, err = store.ListLinks(ctx, sharelink.EntityPost, postID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, []string{link.Token}, tokens(seen), "the second change reads the first one's commit")
	},
}
