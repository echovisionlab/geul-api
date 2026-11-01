package artisttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Harness is one artist.Store implementation the contract runs against,
// with the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[artist.Store]
	// SeedMember stores a Member and returns its ID.
	SeedMember func(t *testing.T) string
	// SeedDocument stores an empty compact content document and returns
	// its ID.
	SeedDocument func(t *testing.T) string
	// SeedLabel stores a Label with its source title, a draft one when
	// draft is set, and returns its ID.
	SeedLabel func(t *testing.T, title string, draft bool) string
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[artist.Store](store),
		SeedMember: func(*testing.T) string {
			id := uuid.NewString()
			store.PutMember(id, "")
			return id
		},
		SeedDocument: func(*testing.T) string { return uuid.NewString() },
		SeedLabel: func(_ *testing.T, title string, draft bool) string {
			id := uuid.NewString()
			store.PutLabel(id, title, draft)
			return id
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store artist.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

// insert stores a draft Artist in its own transaction.
func insert(t *testing.T, h Harness, change func(*artist.Artist)) artist.Artist {
	t.Helper()
	documentID := h.SeedDocument(t)
	value := artist.Artist{SourceLocale: "en", Status: artist.StatusDraft, ContentDocumentID: &documentID}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store artist.Store) error { return store.InsertArtist(ctx, &value) })
	return value
}

func text(value string) *string { return &value }

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing artist answers its sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			_, err := store.FindArtist(ctx, missing)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			_, err = store.ShareLockArtist(ctx, missing)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			_, err = store.LockArtistID(ctx, missing)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			_, err = store.SourceLocale(ctx, missing, artist.UpdateLock)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			_, err = store.ContentDocumentID(ctx, missing, artist.ShareLock)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			_, err = store.DocumentRoot(ctx, missing, artist.KeyShareLock)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			_, err = store.DocumentMetadata(ctx, missing)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			require.ErrorIs(t, store.TouchArtist(ctx, missing, time.Now()), artist.ErrArtistNotFound)
			require.NoError(t, store.StampArtist(ctx, missing, time.Now()), "stamping a missing row checks nothing")
			require.ErrorIs(t, store.DeleteArtist(ctx, missing), artist.ErrArtistNotFound)
			require.ErrorIs(t, store.LockEntity(ctx, missing), entityparticipant.ErrEntityNotFound)
			_, err = store.EntityParent(ctx, missing)
			require.ErrorIs(t, err, entityparticipant.ErrEntityNotFound)
			_, err = store.LocaleTitleText(ctx, missing, "en")
			require.ErrorIs(t, err, artist.ErrLocaleNotFound)
			_, err = store.LocaleOgAssetID(ctx, missing, "en")
			require.ErrorIs(t, err, artist.ErrLocaleNotFound)
			_, err = store.CurrentContentRevision(ctx, uuid.New())
			require.ErrorIs(t, err, artist.ErrDocumentRowNotFound)
			return nil
		})
	},
	"an inserted artist reads back with its ID and created_at": func(t *testing.T, h Harness) {
		value := insert(t, h, func(value *artist.Artist) {
			value.Slug, value.RealName, value.CountryCode = text("inserted-"+uuid.NewString()[:8]), text("Real"), text("KR")
			value.SocialLinks = map[string]string{"site": "https://artist.example"}
		})
		require.NotEmpty(t, value.ID)
		require.False(t, value.CreatedAt.IsZero())
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			stored, err := store.FindArtist(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, stored.ID)
			require.Equal(t, *value.Slug, *stored.Slug)
			require.Equal(t, "Real", *stored.RealName)
			require.Equal(t, map[string]string{"site": "https://artist.example"}, stored.SocialLinks)
			require.Equal(t, artist.StatusDraft, stored.Status)
			require.Equal(t, "en", stored.SourceLocale)
			require.Equal(t, value.CreatedAt.Truncate(time.Microsecond).UTC(), stored.CreatedAt.UTC())
			documentID, err := store.ContentDocumentID(ctx, value.ID, artist.Unlocked)
			require.NoError(t, err)
			require.Equal(t, *value.ContentDocumentID, *documentID)
			root, err := store.DocumentRoot(ctx, value.ID, artist.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, value.ID, root.ID)
			require.Equal(t, artist.StatusDraft, root.Status)
			require.Equal(t, *value.ContentDocumentID, root.DocumentID.String())
			id, err := store.LockArtistID(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, id)
			taken, err := store.SlugTaken(ctx, *value.Slug, "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, *value.Slug, value.ID)
			require.NoError(t, err)
			require.False(t, taken, "only other artists take a slug")
			return nil
		})
	},
	"listing counts every match and keeps published, named, linked and searched artists": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		labelID := h.SeedLabel(t, "Listing label", false)
		published := insert(t, h, func(value *artist.Artist) { value.Status = artist.StatusPublished })
		draft := insert(t, h, nil)
		other := insert(t, h, func(value *artist.Artist) { value.Status = artist.StatusPublished })
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			now := time.Now().UTC()
			require.NoError(t, store.SaveLocaleTitle(ctx, published.ID, "en", "Alpha "+suffix, now))
			require.NoError(t, store.SaveLocaleTitle(ctx, draft.ID, "en", "Alpha "+suffix+" draft", now))
			require.NoError(t, store.SaveLocaleTitle(ctx, other.ID, "en", "Other "+suffix, now))
			return store.ReplaceLabelIDs(ctx, other.ID, []string{labelID})
		})
		search := []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "alpha " + suffix}}
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			artists, total, err := store.ListArtists(ctx, artist.ListPage{PublishedOnly: true, Filters: search, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			require.Len(t, artists, 1)
			require.Equal(t, published.ID, artists[0].ID)
			artists, total, err = store.ListArtists(ctx, artist.ListPage{Filters: search, TieByID: true, Limit: 1})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.Len(t, artists, 1)
			artists, total, err = store.ListArtists(ctx, artist.ListPage{IDs: []string{draft.ID, other.ID}, TieByID: true, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.ElementsMatch(t, []string{draft.ID, other.ID}, []string{artists[0].ID, artists[1].ID})
			artists, total, err = store.ListArtists(ctx, artist.ListPage{
				Filters: []*commonv1.FilterSpec{{Field: "label_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: labelID}}, Limit: 10,
			})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			require.Equal(t, other.ID, artists[0].ID)
			return nil
		})
	},
	"status and updated_at are stored and unpublishing keeps published_at": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		publishedAt := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			require.NoError(t, store.SetArtistStatus(ctx, value.ID, artist.StatusPublished, &publishedAt, publishedAt))
			return store.SetArtistStatus(ctx, value.ID, artist.StatusDraft, nil, publishedAt.Add(time.Second))
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			stored, err := store.ShareLockArtist(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, artist.StatusDraft, stored.Status)
			require.Equal(t, publishedAt, stored.PublishedAt.UTC())
			require.Equal(t, publishedAt.Add(time.Second), stored.UpdatedAt.UTC())
			require.NoError(t, store.TouchArtist(ctx, value.ID, publishedAt.Add(2*time.Second)))
			stored, err = store.FindArtist(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, publishedAt.Add(2*time.Second), stored.UpdatedAt.UTC())
			return nil
		})
	},
	"a metadata change writes only the changed columns": func(t *testing.T, h Harness) {
		parent := insert(t, h, nil)
		value := insert(t, h, func(value *artist.Artist) {
			value.Slug, value.Website, value.RealName = text("metadata-"+uuid.NewString()[:8]), text("https://kept.example"), text("Before")
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			return store.UpdateDocumentMetadata(ctx, value.ID, artist.MetadataChange{
				MetadataChange: entityparticipant.MetadataChange{
					CountryCode: entityparticipant.Assignment{Changed: true, Value: text("JP")},
					Slug:        entityparticipant.Assignment{Changed: true},
					Parent:      entityparticipant.Assignment{Changed: true, Value: &parent.ID},
					SocialLinks: map[string]string{}, SetSocialLinks: true,
				},
				RealName: entityparticipant.Assignment{Changed: true, Value: text("After")},
			}, time.Now().UTC())
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			metadata, err := store.DocumentMetadata(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, artist.DocumentMetadata{
				RealName: text("After"), CountryCode: text("JP"), Website: text("https://kept.example"),
				SocialLinks: map[string]string{}, ParentArtistID: &parent.ID,
			}, metadata)
			parentID, err := store.EntityParent(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, parent.ID, *parentID)
			return nil
		})
	},
	"label links keep their order and are replaced whole": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		first, second := h.SeedLabel(t, "First label", false), h.SeedLabel(t, "Second label", true)
		ordered := []string{second, first}
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			return store.ReplaceLabelIDs(ctx, value.ID, ordered)
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			linked, err := store.LabelIDsInLinkOrder(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, ordered, linked)
			byID, err := store.LabelIDs(ctx, value.ID)
			require.NoError(t, err)
			require.ElementsMatch(t, ordered, byID)
			require.True(t, byID[0] < byID[1], "LabelIDs lists by ID")
			titles, err := store.LabelTitles(ctx, []string{first, second, uuid.NewString()})
			require.NoError(t, err)
			require.Equal(t, map[string]string{first: "First label", second: "Second label"}, titles)
			drafts, err := store.DraftLabelIDs(ctx, ordered)
			require.NoError(t, err)
			require.Equal(t, map[string]bool{second: true}, drafts)
			require.NoError(t, store.ReplaceLabelIDs(ctx, value.ID, []string{first}))
			linked, err = store.LabelIDsInLinkOrder(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, []string{first}, linked)
			return nil
		})
	},
	"locale rows are created, retitled, touched and deleted": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		created := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			require.NoError(t, store.SaveLocaleTitle(ctx, value.ID, "en", "First", created))
			require.NoError(t, store.SaveLocaleTitle(ctx, value.ID, "en", "Renamed", created.Add(time.Second)))
			ok, err := store.CreateTargetLocale(ctx, value.ID, "ko", created)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.EnsureTargetLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.False(t, ok, "an existing locale is left alone")
			ok, err = store.EnsureTargetLocale(ctx, value.ID, "ja")
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.TouchTargetLocale(ctx, value.ID, "ko", created.Add(2*time.Second))
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.SetTargetLocaleTitle(ctx, value.ID, "ja", "日本語", created.Add(3*time.Second))
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.TouchTargetLocale(ctx, value.ID, "fr", created)
			require.NoError(t, err)
			require.False(t, ok)
			ok, err = store.SetTargetLocaleTitle(ctx, value.ID, "fr", "Français", created)
			require.NoError(t, err)
			require.False(t, ok)
			return nil
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			source, exists, err := store.LocaleTitle(ctx, value.ID, "en", true)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "Renamed", *source.Title)
			require.Equal(t, created.Add(time.Second), source.UpdatedAt.UTC())
			target, exists, err := store.LocaleTitle(ctx, value.ID, "ko", false)
			require.NoError(t, err)
			require.True(t, exists)
			require.Nil(t, target.Title)
			require.Equal(t, created.Add(2*time.Second), target.UpdatedAt.UTC())
			title, exists, err := store.LocaleTitleValue(ctx, value.ID, "ja")
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "日本語", *title)
			_, exists, err = store.LocaleTitleValue(ctx, value.ID, "fr")
			require.NoError(t, err)
			require.False(t, exists)
			exists, err = store.LocaleExists(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.True(t, exists)
			text, err := store.LocaleTitleText(ctx, value.ID, "en")
			require.NoError(t, err)
			require.Equal(t, "Renamed", text)
			og, err := store.LocaleOgAssetID(ctx, value.ID, "en")
			require.NoError(t, err)
			require.Nil(t, og)
			deleted, err := store.DeleteTargetLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteTargetLocale(ctx, value.ID, "ko")
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"participants are stored per role and listed Owners first, oldest first": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		owner, manager, late := h.SeedMember(t), h.SeedMember(t), h.SeedMember(t)
		base := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			require.NoError(t, store.InsertParticipant(ctx, value.ID, late, entityparticipant.RoleManager, base.Add(time.Second)))
			require.NoError(t, store.InsertParticipant(ctx, value.ID, manager, entityparticipant.RoleManager, base))
			return store.InsertCreatorOwner(ctx, value.ID, owner, base.Add(time.Minute))
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			require.NoError(t, store.LockEntity(ctx, value.ID))
			role, err := store.ParticipantRole(ctx, value.ID, owner)
			require.NoError(t, err)
			require.Equal(t, entityparticipant.RoleOwner, role)
			role, err = store.ParticipantRole(ctx, value.ID, uuid.NewString())
			require.NoError(t, err)
			require.Equal(t, entityparticipant.RoleNone, role)
			owners, err := store.CountOwners(ctx, value.ID, "")
			require.NoError(t, err)
			require.EqualValues(t, 1, owners)
			participants, err := store.ListParticipants(ctx, value.ID)
			require.NoError(t, err)
			ids := make([]string, len(participants))
			for i, participant := range participants {
				ids[i] = participant.MemberID
			}
			require.Equal(t, []string{owner, manager, late}, ids)
			require.Equal(t, base, participants[1].CreatedAt.UTC())
			deleted, err := store.DeleteParticipant(ctx, value.ID, manager, entityparticipant.RoleManager)
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteParticipant(ctx, value.ID, manager, entityparticipant.RoleManager)
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"the hierarchy finds descendants and children and a deleted parent leaves its children": func(t *testing.T, h Harness) {
		root := insert(t, h, nil)
		child := insert(t, h, func(value *artist.Artist) { value.ParentArtistID = &root.ID })
		grandchild := insert(t, h, func(value *artist.Artist) { value.ParentArtistID = &child.ID })
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			require.NoError(t, store.SaveLocaleTitle(ctx, child.ID, "en", "Child", time.Now().UTC()))
			require.NoError(t, store.LockParentGraph(ctx))
			below, err := store.IsDescendant(ctx, root.ID, grandchild.ID)
			require.NoError(t, err)
			require.True(t, below)
			below, err = store.IsDescendant(ctx, grandchild.ID, root.ID)
			require.NoError(t, err)
			require.False(t, below)
			exists, err := store.EntityExists(ctx, child.ID)
			require.NoError(t, err)
			require.True(t, exists)
			children, err := store.ChildArtists(ctx, root.ID)
			require.NoError(t, err)
			require.Equal(t, []entityparticipant.Related{{ID: child.ID, DisplayName: "Child", RelationCount: 1}}, children)
			return nil
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			return store.DeleteArtist(ctx, child.ID)
		})
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			_, err := store.FindArtist(ctx, child.ID)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			parent, err := store.EntityParent(ctx, grandchild.ID)
			require.NoError(t, err)
			require.Nil(t, parent, "a deleted parent leaves its children at the root")
			return nil
		})
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, h Harness) {
		documentID := h.SeedDocument(t)
		value := artist.Artist{SourceLocale: "en", Status: artist.StatusDraft, ContentDocumentID: &documentID}
		failed := errors.New("later step failed")
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store artist.Store) error {
			if err := store.InsertArtist(ctx, &value); err != nil {
				return err
			}
			if err := store.SaveLocaleTitle(ctx, value.ID, "en", "Rolled back", time.Now().UTC()); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		transact(t, h, func(ctx context.Context, store artist.Store) error {
			_, err := store.FindArtist(ctx, value.ID)
			require.ErrorIs(t, err, artist.ErrArtistNotFound)
			exists, err := store.LocaleExists(ctx, value.ID, "en")
			require.NoError(t, err)
			require.False(t, exists)
			return nil
		})
	},
	"a locked artist makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		publishedAt := time.Now().UTC().Truncate(time.Microsecond)
		var seen artist.Artist
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store artist.Store) error {
				_, err := store.LockArtistID(ctx, value.ID)
				return err
			},
			func(ctx context.Context, store artist.Store) error {
				return store.SetArtistStatus(ctx, value.ID, artist.StatusPublished, &publishedAt, publishedAt)
			},
			func(ctx context.Context, store artist.Store) error {
				if _, err := store.LockArtistID(ctx, value.ID); err != nil {
					return err
				}
				var err error
				seen, err = store.FindArtist(ctx, value.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, artist.StatusPublished, seen.Status, "the second writer reads the first commit")
	},
	"the parent graph lock makes a delete wait for a reparent": func(t *testing.T, h Harness) {
		// A delete locks its root, then the parent graph; a reparent holds
		// the graph, so the delete's relationship snapshot sees the commit
		// that moves the child away.
		parent := insert(t, h, nil)
		child := insert(t, h, func(value *artist.Artist) { value.ParentArtistID = &parent.ID })
		children := []entityparticipant.Related{{ID: "unread"}}
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store artist.Store) error { return store.LockParentGraph(ctx) },
			func(ctx context.Context, store artist.Store) error {
				return store.UpdateDocumentMetadata(ctx, child.ID, artist.MetadataChange{
					MetadataChange: entityparticipant.MetadataChange{Parent: entityparticipant.Assignment{Changed: true}},
				}, time.Now().UTC())
			},
			func(ctx context.Context, store artist.Store) error {
				if err := store.LockEntity(ctx, parent.ID); err != nil {
					return err
				}
				if err := store.LockParentGraph(ctx); err != nil {
					return err
				}
				var err error
				children, err = store.ChildArtists(ctx, parent.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Empty(t, children, "the delete reads the reparent's commit")
	},
}
