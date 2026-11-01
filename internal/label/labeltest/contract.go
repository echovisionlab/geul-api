package labeltest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Harness is one label.Store implementation the contract runs against,
// with the rows of other domains a case needs.
type Harness struct {
	mutationtest.Harness[label.Store]
	// SeedMember stores a Member and returns its ID.
	SeedMember func(t *testing.T) string
	// SeedDocument stores an empty compact content document and returns
	// its ID.
	SeedDocument func(t *testing.T) string
	// SeedFile stores a File and returns its ID.
	SeedFile func(t *testing.T) string
}

// FakeHarness is the Harness of the fake Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[label.Store](store),
		SeedMember: func(*testing.T) string {
			id := uuid.NewString()
			store.PutMember(id, "")
			return id
		},
		SeedDocument: func(*testing.T) string { return uuid.NewString() },
		SeedFile:     func(*testing.T) string { return uuid.NewString() },
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

func transact(t *testing.T, h Harness, work func(ctx context.Context, store label.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}

// insert stores a draft Label in its own transaction.
func insert(t *testing.T, h Harness, change func(*label.Label)) label.Label {
	t.Helper()
	documentID := h.SeedDocument(t)
	value := label.Label{SourceLocale: "en", Status: label.StatusDraft, ContentDocumentID: &documentID}
	if change != nil {
		change(&value)
	}
	transact(t, h, func(ctx context.Context, store label.Store) error { return store.InsertLabel(ctx, &value) })
	return value
}

func text(value string) *string { return &value }

var storeContract = map[string]func(t *testing.T, h Harness){
	"a missing label answers its sentinels": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		transact(t, h, func(ctx context.Context, store label.Store) error {
			_, err := store.FindLabel(ctx, missing)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.FindLabelID(ctx, missing)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.LockLabel(ctx, missing)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.ShareLockLabel(ctx, missing)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.SourceLocale(ctx, missing, label.UpdateLock)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.ContentDocumentID(ctx, missing, label.ShareLock)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.DocumentRoot(ctx, missing, label.KeyShareLock)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			_, err = store.DocumentMetadata(ctx, missing)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			require.ErrorIs(t, store.TouchLabel(ctx, missing, time.Now()), label.ErrLabelNotFound)
			require.ErrorIs(t, store.SetLabelLogo(ctx, missing, label.LogoVariantLight, text(uuid.NewString()), time.Now()), label.ErrLabelNotFound)
			require.ErrorIs(t, store.DeleteLabel(ctx, missing), label.ErrLabelNotFound)
			require.ErrorIs(t, store.LockEntity(ctx, missing), entityparticipant.ErrEntityNotFound)
			_, err = store.EntityParent(ctx, missing)
			require.ErrorIs(t, err, entityparticipant.ErrEntityNotFound)
			effective, err := store.EffectiveLogoFileID(ctx, missing)
			require.NoError(t, err)
			require.Nil(t, effective)
			return nil
		})
	},
	"an inserted label reads back with its ID and timestamps": func(t *testing.T, h Harness) {
		value := insert(t, h, func(value *label.Label) {
			value.Slug, value.CountryCode, value.Website = text("inserted"), text("KR"), text("https://label.example")
			value.SocialLinks = map[string]string{"site": "https://label.example"}
		})
		require.NotEmpty(t, value.ID)
		require.False(t, value.CreatedAt.IsZero())
		transact(t, h, func(ctx context.Context, store label.Store) error {
			stored, err := store.FindLabel(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, stored.ID)
			require.Equal(t, "inserted", *stored.Slug)
			require.Equal(t, "KR", *stored.CountryCode)
			require.Equal(t, map[string]string{"site": "https://label.example"}, stored.SocialLinks)
			require.Equal(t, label.StatusDraft, stored.Status)
			require.Equal(t, "en", stored.SourceLocale)
			require.Equal(t, value.CreatedAt.Truncate(time.Microsecond).UTC(), stored.CreatedAt.UTC())
			documentID, err := store.ContentDocumentID(ctx, value.ID, label.Unlocked)
			require.NoError(t, err)
			require.Equal(t, *value.ContentDocumentID, documentID.String())
			root, err := store.DocumentRoot(ctx, value.ID, label.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, label.DocumentRoot{ID: value.ID, Status: label.StatusDraft, DocumentID: documentID}, root)
			id, err := store.LockLabelID(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, value.ID, id)
			return nil
		})
	},
	"a taken slug fails the insert and only other labels take a slug": func(t *testing.T, h Harness) {
		first := insert(t, h, func(value *label.Label) { value.Slug = text("taken-slug") })
		documentID := h.SeedDocument(t)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store label.Store) error {
			return store.InsertLabel(ctx, &label.Label{SourceLocale: "en", Status: label.StatusDraft, Slug: text("taken-slug"), ContentDocumentID: &documentID})
		})
		require.ErrorIs(t, err, label.ErrSlugTaken)
		transact(t, h, func(ctx context.Context, store label.Store) error {
			taken, err := store.SlugTaken(ctx, "taken-slug", "")
			require.NoError(t, err)
			require.True(t, taken)
			taken, err = store.SlugTaken(ctx, "taken-slug", first.ID)
			require.NoError(t, err)
			require.False(t, taken)
			return nil
		})
	},
	"listing counts every match, keeps published labels when asked and searches source titles": func(t *testing.T, h Harness) {
		suffix := uuid.NewString()[:8]
		published := insert(t, h, func(value *label.Label) { value.Status = label.StatusPublished })
		draft := insert(t, h, nil)
		other := insert(t, h, func(value *label.Label) { value.Status = label.StatusPublished })
		transact(t, h, func(ctx context.Context, store label.Store) error {
			now := time.Now().UTC()
			require.NoError(t, store.SaveLocaleTitle(ctx, published.ID, "en", "Alpha "+suffix, now))
			require.NoError(t, store.SaveLocaleTitle(ctx, draft.ID, "en", "Alpha "+suffix+" draft", now))
			require.NoError(t, store.SaveLocaleTitle(ctx, other.ID, "en", "Other", now))
			return nil
		})
		search := []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "alpha " + suffix}}
		transact(t, h, func(ctx context.Context, store label.Store) error {
			labels, total, err := store.ListLabels(ctx, label.ListPage{PublishedOnly: true, Filters: search, Limit: 10})
			require.NoError(t, err)
			require.EqualValues(t, 1, total)
			require.Len(t, labels, 1)
			require.Equal(t, published.ID, labels[0].ID)
			labels, total, err = store.ListLabels(ctx, label.ListPage{Filters: search, Limit: 1})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.Len(t, labels, 1)
			return nil
		})
	},
	"status, logos and the OG image are stored": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		publishedAt := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store label.Store) error {
			require.NoError(t, store.SetLabelStatus(ctx, value.ID, label.StatusPublished, &publishedAt, publishedAt))
			require.NoError(t, store.SetLabelLogo(ctx, value.ID, label.LogoVariantDark, text(h.SeedFile(t)), publishedAt))
			effective, err := store.EffectiveLogoFileID(ctx, value.ID)
			require.NoError(t, err)
			require.NotNil(t, effective, "the dark logo is effective without a light one")
			light := h.SeedFile(t)
			require.NoError(t, store.SetLabelLogo(ctx, value.ID, label.LogoVariantLight, &light, publishedAt))
			effective, err = store.EffectiveLogoFileID(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, light, *effective)
			require.NoError(t, store.SetLabelStatus(ctx, value.ID, label.StatusDraft, nil, publishedAt.Add(time.Second)))
			require.NoError(t, store.SetLabelLogo(ctx, value.ID, label.LogoVariantLight, nil, publishedAt))
			require.NoError(t, store.ClearOgAsset(ctx, value.ID))
			return nil
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
			stored, err := store.LockLabel(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, label.StatusDraft, stored.Status)
			require.Equal(t, publishedAt, stored.PublishedAt.UTC(), "unpublishing keeps published_at")
			require.Nil(t, stored.LogoLightFileID)
			require.NotNil(t, stored.LogoDarkFileID)
			require.Nil(t, stored.OgAssetID)
			logos, err := store.FindLabelLogos(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, stored.LogoDarkFileID, logos.LogoDarkFileID)
			return nil
		})
	},
	"a metadata change writes only the changed columns": func(t *testing.T, h Harness) {
		parent := insert(t, h, nil)
		value := insert(t, h, func(value *label.Label) {
			value.Slug, value.Website = text("metadata-"+uuid.NewString()[:8]), text("https://kept.example")
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
			return store.UpdateDocumentMetadata(ctx, value.ID, entityparticipant.MetadataChange{
				CountryCode: entityparticipant.Assignment{Changed: true, Value: text("JP")},
				Slug:        entityparticipant.Assignment{Changed: true},
				Parent:      entityparticipant.Assignment{Changed: true, Value: &parent.ID},
				SocialLinks: map[string]string{}, SetSocialLinks: true,
			}, time.Now().UTC())
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
			metadata, err := store.DocumentMetadata(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, label.DocumentMetadata{
				CountryCode: text("JP"), Website: text("https://kept.example"), SocialLinks: map[string]string{}, ParentLabelID: &parent.ID,
			}, metadata)
			parentID, err := store.EntityParent(ctx, value.ID)
			require.NoError(t, err)
			require.Equal(t, parent.ID, *parentID)
			return nil
		})
	},
	"locale rows are created, touched, listed and deleted": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		created := time.Now().UTC().Truncate(time.Microsecond)
		transact(t, h, func(ctx context.Context, store label.Store) error {
			require.NoError(t, store.SaveLocaleTitle(ctx, value.ID, "en", "First", created))
			require.NoError(t, store.SaveLocaleTitle(ctx, value.ID, "en", "Renamed", created.Add(time.Second)))
			ok, err := store.CreateTargetLocale(ctx, value.ID, "ko", created)
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.TouchTargetLocale(ctx, value.ID, "ko", created.Add(2*time.Second))
			require.NoError(t, err)
			require.True(t, ok)
			ok, err = store.TouchTargetLocale(ctx, value.ID, "fr", created)
			require.NoError(t, err)
			require.False(t, ok)
			return nil
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
			titles, err := store.LocaleTitles(ctx, value.ID)
			require.NoError(t, err)
			require.Len(t, titles, 2)
			require.Equal(t, "en", titles[0].Locale)
			require.Equal(t, "Renamed", *titles[0].Title)
			require.Equal(t, "ko", titles[1].Locale)
			require.Nil(t, titles[1].Title)
			require.Equal(t, created.Add(2*time.Second), titles[1].UpdatedAt.UTC())
			target, exists, err := store.LocaleTitle(ctx, value.ID, "ko", true)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, created, target.CreatedAt.UTC())
			_, exists, err = store.LocaleTitle(ctx, value.ID, "fr", false)
			require.NoError(t, err)
			require.False(t, exists)
			title, err := store.LocaleTitleText(ctx, value.ID, "en")
			require.NoError(t, err)
			require.Equal(t, "Renamed", title)
			_, err = store.LocaleTitleText(ctx, value.ID, "fr")
			require.ErrorIs(t, err, label.ErrLocaleNotFound)
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
		transact(t, h, func(ctx context.Context, store label.Store) error {
			require.NoError(t, store.InsertParticipant(ctx, value.ID, late, entityparticipant.RoleManager, base.Add(time.Second)))
			require.NoError(t, store.InsertParticipant(ctx, value.ID, manager, entityparticipant.RoleManager, base))
			require.NoError(t, store.InsertParticipant(ctx, value.ID, owner, entityparticipant.RoleOwner, base.Add(time.Minute)))
			return nil
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
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
			owners, err = store.CountOwners(ctx, value.ID, manager)
			require.NoError(t, err)
			require.Zero(t, owners)
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
	"the hierarchy finds descendants and a deleted parent leaves its children": func(t *testing.T, h Harness) {
		root := insert(t, h, nil)
		child := insert(t, h, func(value *label.Label) { value.ParentLabelID = &root.ID })
		grandchild := insert(t, h, func(value *label.Label) { value.ParentLabelID = &child.ID })
		transact(t, h, func(ctx context.Context, store label.Store) error {
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
			impacts, err := store.DeletionImpacts(ctx, root.ID)
			require.NoError(t, err)
			require.Equal(t, []label.DeletionImpact{{Kind: label.ImpactChildLabel, EntityID: child.ID, DisplayName: ""}}, impacts)
			return nil
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
			return store.DeleteLabel(ctx, child.ID)
		})
		transact(t, h, func(ctx context.Context, store label.Store) error {
			_, err := store.FindLabel(ctx, child.ID)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			parent, err := store.EntityParent(ctx, grandchild.ID)
			require.NoError(t, err)
			require.Nil(t, parent, "a deleted parent leaves its children at the root")
			return nil
		})
	},
	"a failed transaction leaves nothing behind": func(t *testing.T, h Harness) {
		documentID := h.SeedDocument(t)
		value := label.Label{SourceLocale: "en", Status: label.StatusDraft, ContentDocumentID: &documentID}
		failed := errors.New("later step failed")
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, store label.Store) error {
			if err := store.InsertLabel(ctx, &value); err != nil {
				return err
			}
			if err := store.SaveLocaleTitle(ctx, value.ID, "en", "Rolled back", time.Now().UTC()); err != nil {
				return err
			}
			return failed
		})
		require.ErrorIs(t, err, failed)
		transact(t, h, func(ctx context.Context, store label.Store) error {
			_, err := store.FindLabel(ctx, value.ID)
			require.ErrorIs(t, err, label.ErrLabelNotFound)
			titles, err := store.LocaleTitles(ctx, value.ID)
			require.NoError(t, err)
			require.Empty(t, titles)
			return nil
		})
	},
	"a delete waits for a reparent away from the deleted parent": func(t *testing.T, h Harness) {
		// The reparent row-locks its child under the graph lock, and the
		// delete clears its children's parent; with the graph first in both
		// the delete sees the child already moved.
		parent := insert(t, h, nil)
		child := insert(t, h, func(value *label.Label) { value.ParentLabelID = &parent.ID })
		impacts := []label.DeletionImpact{{EntityID: "unread"}}
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store label.Store) error { return store.LockParentGraph(ctx) },
			func(ctx context.Context, store label.Store) error {
				if _, err := store.ContentDocumentID(ctx, child.ID, label.UpdateLock); err != nil {
					return err
				}
				return store.UpdateDocumentMetadata(ctx, child.ID, entityparticipant.MetadataChange{
					Parent: entityparticipant.Assignment{Changed: true},
				}, time.Now().UTC())
			},
			labelDeleteLocks(parent.ID, &impacts),
		)
		require.NoError(t, err)
		require.Empty(t, impacts, "the delete reads the reparent's commit")
	},
	"a delete waits for a reparent onto the deleted parent": func(t *testing.T, h Harness) {
		// Both take the parent graph lock before any Label row (DeleteLabel,
		// the metadata save). The reparent's foreign key share-locks the new
		// parent while it holds the graph; a delete that locked the parent
		// first deadlocked against it.
		parent := insert(t, h, nil)
		child := insert(t, h, nil)
		var impacts []label.DeletionImpact
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store label.Store) error { return store.LockParentGraph(ctx) },
			func(ctx context.Context, store label.Store) error {
				return store.UpdateDocumentMetadata(ctx, child.ID, entityparticipant.MetadataChange{
					Parent: entityparticipant.Assignment{Changed: true, Value: &parent.ID},
				}, time.Now().UTC())
			},
			labelDeleteLocks(parent.ID, &impacts),
		)
		require.NoError(t, err)
		require.Equal(t, []label.DeletionImpact{{Kind: label.ImpactChildLabel, EntityID: child.ID}}, impacts, "the delete reads the reparent's commit")
	},
	"a locked label makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		value := insert(t, h, nil)
		publishedAt := time.Now().UTC().Truncate(time.Microsecond)
		var seen label.Label
		err := mutationtest.RequireSecondWaits(t, h.Harness,
			func(ctx context.Context, store label.Store) error {
				_, err := store.LockLabel(ctx, value.ID)
				return err
			},
			func(ctx context.Context, store label.Store) error {
				return store.SetLabelStatus(ctx, value.ID, label.StatusPublished, &publishedAt, publishedAt)
			},
			func(ctx context.Context, store label.Store) error {
				var err error
				seen, err = store.LockLabel(ctx, value.ID)
				return err
			},
		)
		require.NoError(t, err)
		require.Equal(t, label.StatusPublished, seen.Status, "the second writer reads the first commit")
	},
}

// labelDeleteLocks takes DeleteLabel's locks on parentID, the parent graph,
// the relation tables and the root row, and reads its deletion impacts.
func labelDeleteLocks(parentID string, impacts *[]label.DeletionImpact) func(context.Context, label.Store) error {
	return func(ctx context.Context, store label.Store) error {
		if err := store.LockParentGraph(ctx); err != nil {
			return err
		}
		if err := store.LockLabelRelations(ctx); err != nil {
			return err
		}
		if _, err := store.LockLabelID(ctx, parentID); err != nil {
			return err
		}
		var err error
		*impacts, err = store.DeletionImpacts(ctx, parentID)
		return err
	}
}
