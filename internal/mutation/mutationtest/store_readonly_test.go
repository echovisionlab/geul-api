package mutationtest_test

import (
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/campaign/campaigntest"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailauthoring/emailauthoringtest"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/menu/menutest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	"github.com/echovisionlab/geul-api/internal/sitesettings/sitesettingstest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestReadChecksTheEmbeddedContentStore(t *testing.T) {
	t.Run("campaign", func(t *testing.T) {
		store, _ := campaigntest.New()
		units := mutationtest.New(store)
		id := uuid.New()
		err := units.Read(t.Context(), func(read *campaigntest.Store) error {
			_, err := read.CreateContentDocument(t.Context(), contentblock.CreateInput{ID: id, SourceLocale: "en"})
			return err
		})
		require.ErrorIs(t, err, mutationtest.ErrReadOnly)
		_, found := store.Document(id)
		require.False(t, found)
	})
	t.Run("email authoring", func(t *testing.T) {
		store, _ := emailauthoringtest.New()
		units := mutationtest.New(store)
		id := uuid.New()
		err := units.Read(t.Context(), func(read *emailauthoringtest.Store) error {
			_, err := read.CreateContentDocument(t.Context(), contentblock.CreateInput{ID: id, SourceLocale: "en"})
			return err
		})
		require.ErrorIs(t, err, mutationtest.ErrReadOnly)
		_, found := store.Document(id)
		require.False(t, found)
	})
	t.Run("file media assets", func(t *testing.T) {
		store := filemediatest.NewLibraryStore()
		units := mutationtest.New(store)
		id := uuid.NewString()
		err := units.Read(t.Context(), func(read *filemediatest.LibraryStore) error {
			return read.MediaAssets.InsertAsset(t.Context(), mediaasset.Asset{ID: id, ObjectKey: id, Status: mediaasset.AssetAllocated})
		})
		require.ErrorIs(t, err, mutationtest.ErrReadOnly)
		_, err = store.MediaAssets.Asset(t.Context(), id)
		require.ErrorIs(t, err, mediaasset.ErrAssetNotFound)
	})
}

func TestReadRestoresNestedContentReturnedByTheStore(t *testing.T) {
	store := emailcontenttest.New()
	parent := uuid.New()
	expectedParent := parent
	document := store.PutDocument(emailcontenttest.Document{
		Blocks:   []contentblock.BaseBlock{{ID: uuid.New(), ParentID: &parent, SharedData: []byte(`{"value":"before"}`)}},
		Overlays: map[string][]contentblock.LocaleBlockUpdate{"ko": {{LocalizedData: []byte(`{"value":"before"}`)}}},
	})
	units := mutationtest.New(store)
	err := units.Read(t.Context(), func(read *emailcontenttest.Store) error {
		snapshot, err := read.ContentSnapshot(t.Context(), document.ID, "en")
		if err != nil {
			return err
		}
		snapshot.Blocks[0].SharedData[0] = '!'
		*snapshot.Blocks[0].ParentID = uuid.New()
		snapshot.LocaleOverlays[0].Blocks[0].LocalizedData[0] = '!'
		return nil
	})
	require.ErrorIs(t, err, mutationtest.ErrReadOnly)
	restored, found := store.Document(document.ID)
	require.True(t, found)
	require.Equal(t, []byte(`{"value":"before"}`), []byte(restored.Blocks[0].SharedData))
	require.Equal(t, expectedParent, *restored.Blocks[0].ParentID)
	require.Equal(t, []byte(`{"value":"before"}`), []byte(restored.Overlays["ko"][0].LocalizedData))
}

func TestReadChecksMenuPortWrites(t *testing.T) {
	store := menutest.NewStore()
	units := mutationtest.New(store)
	require.ErrorIs(t, units.Read(t.Context(), func(read *menutest.Store) error {
		return read.ClearSiteSettingsMenu(t.Context(), "menu")
	}), mutationtest.ErrReadOnly)
	require.Empty(t, store.ClearedSiteSettings())
	require.ErrorIs(t, units.Read(t.Context(), func(read *menutest.Store) error {
		return read.PublishAIDocumentChange(t.Context(), menu.AIDocumentChange{MenuID: "menu", Changed: true})
	}), mutationtest.ErrReadOnly)
	require.Empty(t, store.Signals())
}

func TestReadDistinguishesOGRunCreationFromRequestObservations(t *testing.T) {
	store, _ := sitesettingstest.New()
	units := mutationtest.New(store)
	before := store.Stored()
	require.NoError(t, units.Read(t.Context(), func(read *sitesettingstest.Store) error {
		runID, err := read.RequestOGRegeneration(t.Context(), before, before, []string{"site_title"})
		require.Nil(t, runID)
		return err
	}))
	after := before.Clone()
	after.SiteTitle = "changed"
	require.ErrorIs(t, units.Read(t.Context(), func(read *sitesettingstest.Store) error {
		_, err := read.RequestOGRegeneration(t.Context(), before, after, []string{"site_title"})
		return err
	}), mutationtest.ErrReadOnly)
	require.NoError(t, units.ReadCurrent(t.Context(), func(read *sitesettingstest.Store) error {
		runID, err := read.RequestOGRegeneration(t.Context(), before, after, []string{"site_title"})
		require.NotNil(t, runID)
		require.Equal(t, "og-run-1", *runID)
		return err
	}))
	require.Len(t, store.OGRequests(), 3, "request observations survive rollback")
}

func TestReadDoesNotTreatOneShotFailuresAsPersistedWrites(t *testing.T) {

	t.Run("page failure", func(t *testing.T) {
		store := pagetest.NewStore()
		units := mutationtest.New(store)
		failure := errors.New("one-shot page read failure")
		store.FailOnce = map[string]error{"RuntimeSettings": failure}
		err := units.Read(t.Context(), func(read *pagetest.Store) error { _, err := read.RuntimeSettings(t.Context()); return err })
		require.Same(t, failure, err)
		require.NoError(t, units.Read(t.Context(), func(read *pagetest.Store) error { _, err := read.RuntimeSettings(t.Context()); return err }))
	})
}
