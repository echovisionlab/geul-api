package og_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/og/ogtest"
)

func TestRequestBulkQueuesOneGenerationPerTargetInRequestOrder(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	plan := mustPlan(t, units,
		localeRequest("post", entityID, "한국어 제목", "ko"),
		localeRequest("post", entityID, "Titre FR", "fr"),
	)
	require.Len(t, plan.GenerationIDs, 2)
	require.NotEqual(t, plan.GenerationIDs[0], plan.GenerationIDs[1])

	for i, want := range []struct{ locale, title string }{{"ko", "한국어 제목"}, {"fr", "Titre FR"}} {
		generation := generation(t, store, plan.GenerationIDs[i])
		assert.Equal(t, og.StatusQueued, generation.Status)
		assert.Equal(t, plan.RunID, generation.RunID)
		snapshot := snapshotOf(t, generation)
		assert.Equal(t, want.locale, *snapshot.Locale)
		assert.Equal(t, want.title, snapshot.Title)
		assert.Equal(t, generation.ID, snapshot.Output.AssetID)
		assert.Equal(t, "asset/"+generation.ID+".webp", snapshot.Output.ObjectKey)
		target, ok := store.StoredTarget("post", entityID, &want.locale)
		require.True(t, ok)
		assert.Equal(t, generation.ID, *target.LatestGenerationID)
		asset, ok := store.StoredAsset(generation.ID)
		require.True(t, ok)
		assert.Equal(t, og.AssetAllocated, asset.Status)
		assert.Equal(t, "og", asset.Kind)
	}
	stored := run(t, store, plan.RunID)
	assert.Equal(t, og.RunQueued, stored.Status)
	assert.Equal(t, ogtest.Revision(stored.RenderConfigSnapshot), stored.ConfigRevision)
	assert.Equal(t, plan.GenerationIDs, store.Enqueued())
	assert.Equal(t, []og.GenerationStatus{og.StatusQueued, og.StatusQueued}, statuses(store.Notifications()))
	assert.Len(t, store.Released(), 2, "a pending render releases the projection it replaces")
}

func TestRequestBulkSupersedesTheTargetsPreviousGeneration(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	requests := []og.Request{localeRequest("post", entityID, "KO", "ko"), localeRequest("post", entityID, "FR", "fr")}
	first := mustPlan(t, units, requests...)
	second := mustPlan(t, units, requests...)

	for i, firstID := range first.GenerationIDs {
		old := generation(t, store, firstID)
		assert.Equal(t, og.StatusSuperseded, old.Status)
		assert.Equal(t, second.GenerationIDs[i], *old.SupersededByID)
	}
	assert.Equal(t, og.RunCancelled, run(t, store, first.RunID).Status, "a fully superseded run settles cancelled")
	notifications := store.Notifications()[2:]
	assert.Equal(t, []og.GenerationStatus{og.StatusSuperseded, og.StatusSuperseded, og.StatusQueued, og.StatusQueued}, statuses(notifications))
	assert.Equal(t, second.GenerationIDs[0], *notifications[0].ReplacementID)
}

func TestRequestBulkSnapshotsTheNewestReadyFeaturedImage(t *testing.T) {
	store, units := ogtest.New()
	fileID := uuid.NewString()
	older := ogtest.ReadyAsset("image", time.Now().Add(-time.Hour))
	older.SourceFileID = &fileID
	newer := ogtest.ReadyAsset("image", time.Now())
	newer.SourceFileID = &fileID
	store.SeedAsset(older)
	store.SeedAsset(newer)
	request := localeRequest("work", "work-1", "Inspire Resort: Le Space", "en")
	request.FeaturedImageFileID = &fileID

	plan := mustPlan(t, units, request)
	snapshot := snapshotOf(t, generation(t, store, plan.GenerationIDs[0]))
	require.NotNil(t, snapshot.FeaturedImage)
	assert.Equal(t, newer.ID, snapshot.FeaturedImage.AssetID)
	assert.Contains(t, snapshot.FeaturedImage.URL, newer.ID)
	assert.Equal(t, "work-1", snapshot.EntityID)
}

func TestRequestBulkRollsBackTheWholeRunWhenAFeaturedImageIsMissing(t *testing.T) {
	store, units := ogtest.New()
	request := entityRequest("label", uuid.NewString(), "Label")
	request.FeaturedImageFileID = new(uuid.NewString())

	_, err := requestBulk(t, units, "rollback", localeRequest("post", uuid.NewString(), "Post", "en"), request)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.ErrorContains(t, err, "public_asset_for_source_file")
	require.Equal(t, mutation.StepRollback, units.Steps()[len(units.Steps())-1])
	require.Empty(t, store.Enqueued())
	_, found := store.StoredTarget("label", request.EntityID, nil)
	require.False(t, found)
}

func TestRequestBulkFailsWhenTheReloadedTargetSetChanged(t *testing.T) {
	_, units := ogtest.New()
	entityID := uuid.NewString()
	requests := []og.Request{localeRequest("post", entityID, "KO", "ko")}
	for name, reloaded := range map[string][]og.Request{
		"a target disappeared": nil,
		"another target":       {localeRequest("post", entityID, "FR", "fr")},
	} {
		t.Run(name, func(t *testing.T) {
			err := units.Transaction(t.Context(), false, func(tx mutation.Tx[og.Store]) error {
				_, err := og.RequestBulk(t.Context(), tx.Store(), dbtime.Now(), "automatic", "reload", requests, sameRequests(reloaded))
				return err
			})
			require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
		})
	}
}

func TestRequestBulkSnapshotsTheReloadedTitle(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	var plan *og.Plan
	require.NoError(t, units.Transaction(t.Context(), false, func(tx mutation.Tx[og.Store]) (err error) {
		plan, err = og.RequestBulk(t.Context(), tx.Store(), dbtime.Now(), "automatic", "reload",
			[]og.Request{localeRequest("post", entityID, "Stale", "ko")},
			sameRequests([]og.Request{localeRequest("post", entityID, "Current", "ko")}))
		return err
	}))
	assert.Equal(t, "Current", snapshotOf(t, generation(t, store, plan.GenerationIDs[0])).Title)
}

func TestRequestBulkRejectsInvalidRequestsBeforeWriting(t *testing.T) {
	entityID := uuid.NewString()
	for name, test := range map[string]struct {
		trigger, reason string
		requests        []og.Request
		reload          og.Reload
	}{
		"no reloader":              {trigger: "manual", reason: "r", requests: []og.Request{localeRequest("post", entityID, "", "ko")}},
		"no trigger":               {trigger: " ", reason: "r", requests: []og.Request{localeRequest("post", entityID, "", "ko")}, reload: sameRequests(nil)},
		"no reason":                {trigger: "manual", reason: " ", requests: []og.Request{localeRequest("post", entityID, "", "ko")}, reload: sameRequests(nil)},
		"no targets":               {trigger: "manual", reason: "r", reload: sameRequests(nil)},
		"an entity with a locale":  {trigger: "manual", reason: "r", requests: []og.Request{{Target: og.Target{EntityType: "label", EntityID: entityID, Locale: new("ko"), Kind: og.KindEntity}}}, reload: sameRequests(nil)},
		"a locale without one":     {trigger: "manual", reason: "r", requests: []og.Request{{Target: og.Target{EntityType: "post", EntityID: entityID, Kind: og.KindLocale}}}, reload: sameRequests(nil)},
		"an unknown kind":          {trigger: "manual", reason: "r", requests: []og.Request{{Target: og.Target{EntityType: "post", EntityID: entityID, Kind: "other"}}}, reload: sameRequests(nil)},
		"an unsupported locale":    {trigger: "manual", reason: "r", requests: []og.Request{localeRequest("post", entityID, "", "xx-YY")}, reload: sameRequests(nil)},
		"a duplicate target":       {trigger: "manual", reason: "r", requests: []og.Request{localeRequest("post", entityID, "", "ko"), localeRequest("post", entityID, "", "ko")}, reload: sameRequests(nil)},
		"a target without an id":   {trigger: "manual", reason: "r", requests: []og.Request{localeRequest("post", " ", "", "ko")}, reload: sameRequests(nil)},
		"a target without a type ": {trigger: "manual", reason: "r", requests: []og.Request{localeRequest(" ", entityID, "", "ko")}, reload: sameRequests(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			store, units := ogtest.New()
			err := units.Transaction(t.Context(), false, func(tx mutation.Tx[og.Store]) error {
				_, err := og.RequestBulk(t.Context(), tx.Store(), dbtime.Now(), test.trigger, test.reason, test.requests, test.reload)
				return err
			})
			require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
			require.Empty(t, store.Enqueued())
		})
	}
}

func TestRequestBulkFailsWhenEnqueueFails(t *testing.T) {
	store, units := ogtest.New()
	queueDown := errors.New("queue down")
	store.Err["EnqueueGeneration"] = queueDown
	_, err := requestBulk(t, units, "enqueue", localeRequest("post", uuid.NewString(), "", "ko"))
	require.ErrorIs(t, err, queueDown)
	require.Equal(t, mutation.StepRollback, units.Steps()[len(units.Steps())-1])
}

func TestRequestCurrentResolvesTheEntityAndRereadsItAfterLocking(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	var selections []og.Selection
	store.Sources["work"] = func(id string, selection og.Selection) ([]og.Request, error) {
		selections = append(selections, selection)
		return []og.Request{localeRequest("work", id, "Work", "en")}, nil
	}
	var plan *og.Plan
	require.NoError(t, units.Transaction(t.Context(), false, func(tx mutation.Tx[og.Store]) (err error) {
		plan, err = og.RequestCurrent(t.Context(), tx.Store(), dbtime.Now(), "work", entityID, og.RefreshSelection("", true), "work_updated")
		return err
	}))
	require.Len(t, plan.GenerationIDs, 1)
	require.Equal(t, []og.Selection{{Kind: og.SelectionAllLocales}, {Kind: og.SelectionAllLocales}}, selections)
	require.Equal(t, "automatic", run(t, store, plan.RunID).TriggerKind)
	require.Equal(t, "work_updated", run(t, store, plan.RunID).Reason)
}

func TestResolveRequestsAnswersPolicyAndMissingEntities(t *testing.T) {
	store, _ := ogtest.New()
	called := false
	store.Sources["release"] = func(string, og.Selection) ([]og.Request, error) {
		called = true
		return nil, nil
	}
	store.Sources["work"] = func(string, og.Selection) ([]og.Request, error) { return nil, og.ErrEntityNotFound }

	_, err := og.ResolveRequests(t.Context(), store, "release", "release-1", og.Selection{Kind: og.SelectionPrimary})
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	require.False(t, called, "legacy entities never reach their source")
	_, err = og.ResolveRequests(t.Context(), store, "work", " work-1 ", og.Selection{Kind: og.SelectionPrimary})
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.ErrorContains(t, err, "work-1")
	_, err = og.ResolveRequests(t.Context(), store, "OG_ENTITY_TYPE_UNSPECIFIED", "id", og.Selection{})
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
}

func TestRefreshSelectionPrefersOneLocaleThenAllLocales(t *testing.T) {
	assert.Equal(t, og.Selection{Kind: og.SelectionLocale, Locale: "ko"}, og.RefreshSelection(" ko ", true))
	assert.Equal(t, og.Selection{Kind: og.SelectionAllLocales}, og.RefreshSelection(" ", true))
	assert.Equal(t, og.Selection{Kind: og.SelectionPrimary}, og.RefreshSelection("", false))
}

func TestCollectRequestsKeepsOneRequestPerTarget(t *testing.T) {
	store, _ := ogtest.New()
	entityID := uuid.NewString()
	store.All = []og.Request{
		localeRequest("post", entityID, "first", "ko"),
		localeRequest("post", entityID, "second", "ko"),
		localeRequest("post", entityID, "fr", "fr"),
	}
	requests, err := og.CollectRequests(context.Background(), store)
	require.NoError(t, err)
	require.Len(t, requests, 2)
	assert.Equal(t, "first", requests[0].Title)
}
