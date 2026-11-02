package og_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/og/ogtest"
)

func TestClaimLeasesAQueuedGenerationAndStartsItsRun(t *testing.T) {
	store, units := ogtest.New()
	plan := mustPlan(t, units, localeRequest("work", uuid.NewString(), "Work", "en"))

	claim, err := og.NewLifecycle(units).Claim(t.Context(), " "+plan.GenerationIDs[0]+" ")
	require.NoError(t, err)
	require.Equal(t, og.Claimed, claim.Result)
	require.NotEmpty(t, claim.LeaseToken)
	require.Equal(t, og.StatusProcessing, claim.Generation.Status)
	require.Equal(t, "Work", claim.EntitySnapshot.Title)
	require.JSONEq(t, `{"site_title":"","primary_color":"#b02d23"}`, string(claim.RenderConfigSnapshot))
	require.Equal(t, ogtest.Revision(claim.RenderConfigSnapshot), claim.ConfigRevision)
	stored := generation(t, store, plan.GenerationIDs[0])
	require.Equal(t, claim.LeaseToken, *stored.LeaseToken)
	require.WithinDuration(t, time.Now().Add(10*time.Minute), *stored.LeaseExpiresAt, time.Minute)
	started := run(t, store, plan.RunID)
	require.Equal(t, og.RunRunning, started.Status)
	require.NotNil(t, started.StartedAt)
	require.Equal(t, og.StatusProcessing, store.Notifications()[len(store.Notifications())-1].Status)
}

func TestClaimExposesAnActiveLeaseAndReclaimsOnlyAfterExpiry(t *testing.T) {
	store, units := ogtest.New()
	plan := mustPlan(t, units, localeRequest("work", uuid.NewString(), "Work", "en"))
	id := plan.GenerationIDs[0]
	activeUntil := time.Now().Add(time.Minute)
	lease := leased(store, id, activeUntil)
	lifecycle := og.NewLifecycle(units)

	active, err := lifecycle.Claim(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, og.ClaimSkipped, active.Result)
	require.Equal(t, og.StatusProcessing, active.Generation.Status)
	require.True(t, active.LeaseExpiresAt.Equal(activeUntil))
	require.Empty(t, active.LeaseToken)

	store.UpdateGeneration(id, func(generation *og.Generation) { generation.LeaseExpiresAt = new(time.Now().Add(-time.Second)) })
	reclaimed, err := lifecycle.Claim(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, og.Claimed, reclaimed.Result)
	require.NotEqual(t, lease, reclaimed.LeaseToken)

	require.NoError(t, lifecycle.Fail(t.Context(), id, reclaimed.LeaseToken, og.FailureCodeProcessingFailed))
	terminal, err := lifecycle.Claim(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, og.ClaimSkipped, terminal.Result)
	require.Equal(t, og.StatusFailed, terminal.Generation.Status)
	require.Nil(t, terminal.LeaseExpiresAt)
}

func TestClaimSettlesGenerationsThatCanNoLongerRender(t *testing.T) {
	t.Run("a target with no latest generation fails the claim", func(t *testing.T) {
		store, units := ogtest.New()
		entityID := uuid.NewString()
		plan := mustPlan(t, units, localeRequest("work", entityID, "Work", "en"))
		target, _ := store.StoredTarget("work", entityID, new("en"))
		require.NoError(t, units.Transaction(t.Context(), false, txClear(t, target.ID)))

		claim, err := og.NewLifecycle(units).Claim(t.Context(), plan.GenerationIDs[0])
		require.NoError(t, err)
		require.Equal(t, og.ClaimSkipped, claim.Result)
		require.Equal(t, og.StatusFailed, claim.Generation.Status)
		require.Equal(t, og.FailureCodeInvalidClaim, *generation(t, store, plan.GenerationIDs[0]).LastErrorCode)
	})
	t.Run("a replaced generation is superseded by the latest", func(t *testing.T) {
		store, units := ogtest.New()
		request := localeRequest("work", uuid.NewString(), "Work", "en")
		first := mustPlan(t, units, request)
		second := mustPlan(t, units, request)
		store.UpdateGeneration(first.GenerationIDs[0], func(generation *og.Generation) { generation.Status = og.StatusQueued })

		claim, err := og.NewLifecycle(units).Claim(t.Context(), first.GenerationIDs[0])
		require.NoError(t, err)
		require.Equal(t, og.ClaimSkipped, claim.Result)
		require.Equal(t, og.StatusSuperseded, claim.Generation.Status)
		require.Equal(t, second.GenerationIDs[0], *generation(t, store, first.GenerationIDs[0]).SupersededByID)
	})
	t.Run("a generation past its deadline fails", func(t *testing.T) {
		store, units := ogtest.New()
		plan := mustPlan(t, units, localeRequest("work", uuid.NewString(), "Work", "en"))
		store.UpdateGeneration(plan.GenerationIDs[0], func(generation *og.Generation) { generation.DeadlineAt = time.Now().Add(-time.Second) })

		claim, err := og.NewLifecycle(units).Claim(t.Context(), plan.GenerationIDs[0])
		require.NoError(t, err)
		require.Equal(t, og.StatusFailed, claim.Generation.Status)
		require.Equal(t, og.FailureCodeProcessingFailed, *generation(t, store, plan.GenerationIDs[0]).LastErrorCode)
	})
}

func TestClaimOfATerminalGenerationNeverRewritesItsHistory(t *testing.T) {
	for _, terminal := range []og.GenerationStatus{og.StatusReady, og.StatusFailed, og.StatusCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			store, units := ogtest.New()
			entityID := uuid.NewString()
			request := localeRequest("work", entityID, "Work", "en")
			first := mustPlan(t, units, request)
			id := first.GenerationIDs[0]
			lifecycle := og.NewLifecycle(units)
			switch terminal {
			case og.StatusReady:
				status, _, err := lifecycle.Complete(t.Context(), id, leased(store, id, time.Now().Add(time.Minute)), written(id))
				require.NoError(t, err)
				require.Equal(t, og.StatusReady, status)
			case og.StatusFailed:
				require.NoError(t, lifecycle.Fail(t.Context(), id, leased(store, id, time.Now().Add(time.Minute)), og.FailureCodeProcessingFailed))
			case og.StatusCancelled:
				require.NoError(t, cancelEntity(t, units, "work", entityID))
			}
			settled := run(t, store, first.RunID)
			mustPlan(t, units, request)

			claim, err := lifecycle.Claim(t.Context(), id)
			require.NoError(t, err)
			require.Equal(t, og.ClaimSkipped, claim.Result)
			require.Equal(t, terminal, claim.Generation.Status)
			require.Equal(t, terminal, generation(t, store, id).Status)
			require.Equal(t, settled, run(t, store, first.RunID))
		})
	}
}

func TestClaimAnswersMalformedAndMissingGenerations(t *testing.T) {
	_, units := ogtest.New()
	_, err := og.NewLifecycle(units).Claim(t.Context(), "not-a-uuid")
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	missing := uuid.NewString()
	_, err = og.NewLifecycle(units).Claim(t.Context(), missing)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.ErrorContains(t, err, missing)
}

func TestCompleteBindsTheLatestGenerationAndSettlesItsRun(t *testing.T) {
	store, units := ogtest.New()
	request := localeRequest("work", uuid.NewString(), "Work", "en")
	plan := mustPlan(t, units, request)
	id := plan.GenerationIDs[0]

	status, asset, err := og.NewLifecycle(units).Complete(t.Context(), id, leased(store, id, time.Now().Add(time.Minute)), written(id))
	require.NoError(t, err)
	require.Equal(t, og.StatusReady, status)
	require.Equal(t, id, asset.AssetID)
	require.Equal(t, ogtest.CDNDomain+"/asset/"+id+".webp", asset.URL)
	require.EqualValues(t, 123, asset.FileSize)
	pointed, ok := store.ProjectionAsset(request.Target)
	require.True(t, ok)
	require.Equal(t, id, pointed)
	stored := generation(t, store, id)
	require.Equal(t, og.StatusReady, stored.Status)
	require.NotNil(t, stored.ReadyAt)
	require.Equal(t, og.RunReady, run(t, store, plan.RunID).Status)
	notifications := store.Notifications()
	ready := notifications[len(notifications)-1]
	require.Equal(t, og.StatusReady, ready.Status)
	require.Equal(t, id, ready.Asset.AssetID)
}

func TestTheNewerCompletionWinsOverASupersededOne(t *testing.T) {
	store, units := ogtest.New()
	request := localeRequest("work", uuid.NewString(), "Work", "en")
	first := mustPlan(t, units, request)
	firstLease := leased(store, first.GenerationIDs[0], time.Now().Add(time.Minute))
	second := mustPlan(t, units, request)
	secondLease := leased(store, second.GenerationIDs[0], time.Now().Add(time.Minute))
	lifecycle := og.NewLifecycle(units)

	status, _, err := lifecycle.Complete(t.Context(), second.GenerationIDs[0], secondLease, written(second.GenerationIDs[0]))
	require.NoError(t, err)
	require.Equal(t, og.StatusReady, status)
	status, asset, err := lifecycle.Complete(t.Context(), first.GenerationIDs[0], firstLease, written(first.GenerationIDs[0]))
	require.NoError(t, err)
	require.Equal(t, og.StatusSuperseded, status)
	require.NotNil(t, asset, "a replayed superseded completion still finishes its asset")

	pointed, _ := store.ProjectionAsset(request.Target)
	require.Equal(t, second.GenerationIDs[0], pointed)
	bound, err := store.AssetBound(t.Context(), first.GenerationIDs[0])
	require.NoError(t, err)
	require.False(t, bound)
}

func TestCompleteOfARemovedLocaleFailsWithoutSupersedingItself(t *testing.T) {
	store, units := ogtest.New()
	request := localeRequest("post", uuid.NewString(), "Post", "fr")
	plan := mustPlan(t, units, request)
	id := plan.GenerationIDs[0]
	store.RemoveProjectionRow(request.Target)

	status, _, err := og.NewLifecycle(units).Complete(t.Context(), id, leased(store, id, time.Now().Add(time.Minute)), written(id))
	require.NoError(t, err)
	require.Equal(t, og.StatusFailed, status)
	stored := generation(t, store, id)
	require.Equal(t, og.FailureCodeCompletionRejected, *stored.LastErrorCode)
	require.Nil(t, stored.LeaseToken)
	require.Nil(t, stored.SupersededByID)
	require.NotNil(t, stored.CompletedAt)
}

func TestCompletePastTheDeadlineStaysFailedAndUnbound(t *testing.T) {
	store, units := ogtest.New()
	request := localeRequest("work", uuid.NewString(), "Work", "en")
	id := mustPlan(t, units, request).GenerationIDs[0]
	lease := leased(store, id, time.Now().Add(time.Hour))
	store.UpdateGeneration(id, func(generation *og.Generation) { generation.DeadlineAt = time.Now().Add(-time.Second) })

	status, _, err := og.NewLifecycle(units).Complete(t.Context(), id, lease, written(id))
	require.NoError(t, err)
	assert.Equal(t, og.StatusFailed, status)
	stored := generation(t, store, id)
	assert.Equal(t, og.FailureCodeCompletionRejected, *stored.LastErrorCode)
	assert.Nil(t, stored.ReadyAt)
	_, pointed := store.ProjectionAsset(request.Target)
	assert.False(t, pointed)
}

func TestCompleteRejectsAMismatchedWriteOrLease(t *testing.T) {
	store, units := ogtest.New()
	id := mustPlan(t, units, localeRequest("work", uuid.NewString(), "Work", "en")).GenerationIDs[0]
	lifecycle := og.NewLifecycle(units)

	_, _, err := lifecycle.Complete(t.Context(), id, "lease", nil)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	_, _, err = lifecycle.Complete(t.Context(), id, "lease", written(uuid.NewString()))
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	_, _, err = lifecycle.Complete(t.Context(), id, "lease", written(id))
	require.ErrorContains(t, err, "not completable")

	lease := leased(store, id, time.Now().Add(time.Minute))
	_, _, err = lifecycle.Complete(t.Context(), id, uuid.NewString(), written(id))
	require.ErrorContains(t, err, "lease is not active")
	store.UpdateGeneration(id, func(generation *og.Generation) { generation.LeaseExpiresAt = new(time.Now().Add(-time.Second)) })
	_, _, err = lifecycle.Complete(t.Context(), id, lease, written(id))
	require.ErrorContains(t, err, "lease has expired")
	require.Equal(t, og.StatusProcessing, generation(t, store, id).Status)

	store.UpdateGeneration(id, func(generation *og.Generation) { generation.LeaseExpiresAt = new(time.Now().Add(time.Minute)) })
	_, _, err = lifecycle.Complete(t.Context(), id, lease, written(id))
	require.NoError(t, err)
	_, _, err = lifecycle.Complete(t.Context(), id, uuid.NewString(), written(id))
	require.ErrorContains(t, err, "completion lease does not match")
	status, asset, err := lifecycle.Complete(t.Context(), id, " "+lease+" ", written(id))
	require.NoError(t, err)
	require.Equal(t, og.StatusReady, status)
	require.Equal(t, id, asset.AssetID)
}

func TestFailRecordsABoundedCodeOnlyForTheLatestLeasedGeneration(t *testing.T) {
	store, units := ogtest.New()
	request := localeRequest("work", uuid.NewString(), "Work", "en")
	first := mustPlan(t, units, request)
	lifecycle := og.NewLifecycle(units)
	lease := leased(store, first.GenerationIDs[0], time.Now().Add(time.Minute))

	err := lifecycle.Fail(t.Context(), first.GenerationIDs[0], lease, "made_up")
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	err = lifecycle.Fail(t.Context(), first.GenerationIDs[0], uuid.NewString(), og.FailureCodeSourceRejected)
	require.ErrorContains(t, err, "lease is not active")

	second := mustPlan(t, units, request)
	require.NoError(t, lifecycle.Fail(t.Context(), first.GenerationIDs[0], lease, og.FailureCodeSourceRejected),
		"a superseded generation's failure is moot")
	require.Equal(t, og.StatusSuperseded, generation(t, store, first.GenerationIDs[0]).Status)

	lease = leased(store, second.GenerationIDs[0], time.Now().Add(time.Minute))
	require.NoError(t, lifecycle.Fail(t.Context(), second.GenerationIDs[0], " "+lease+" ", " "+og.FailureCodeSourceRejected+" "))
	failed := generation(t, store, second.GenerationIDs[0])
	require.Equal(t, og.StatusFailed, failed.Status)
	require.Equal(t, og.FailureCodeSourceRejected, *failed.LastErrorCode)
	require.Equal(t, og.RunFailed, run(t, store, second.RunID).Status)
	status, err := lifecycle.GenerationStatus(t.Context(), second.GenerationIDs[0])
	require.NoError(t, err)
	require.Equal(t, og.StatusFailed, status)
}

func TestCancelEntityCancelsEveryLocaleAndClearsItsTargets(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	plan := mustPlan(t, units, localeRequest("post", entityID, "KO", "ko"), localeRequest("post", entityID, "FR", "fr"))

	require.NoError(t, cancelEntity(t, units, "post", entityID))
	for _, id := range plan.GenerationIDs {
		cancelled := generation(t, store, id)
		assert.Equal(t, og.StatusCancelled, cancelled.Status)
		assert.NotNil(t, cancelled.CancelledAt)
	}
	for _, locale := range []string{"ko", "fr"} {
		target, ok := store.StoredTarget("post", entityID, &locale)
		require.True(t, ok)
		assert.Nil(t, target.LatestGenerationID)
	}
	assert.Equal(t, og.RunCancelled, run(t, store, plan.RunID).Status)
}

func TestCancellingPartOfARunStartsItWhileOthersStillRun(t *testing.T) {
	store, units := ogtest.New()
	first, second := uuid.NewString(), uuid.NewString()
	plan := mustPlan(t, units, localeRequest("work", first, "First", "en"), localeRequest("work", second, "Second", "en"))

	require.NoError(t, cancelEntity(t, units, "work", first))
	stored := run(t, store, plan.RunID)
	require.Equal(t, og.RunRunning, stored.Status)
	require.NotNil(t, stored.StartedAt)
	require.Nil(t, stored.CompletedAt)
	require.Equal(t, og.StatusQueued, generation(t, store, plan.GenerationIDs[1]).Status)
}

func TestCancelEntityRejectsNonCanonicalSiteAndLegalTargets(t *testing.T) {
	_, units := ogtest.New()
	for _, entityType := range []string{"site", "privacy", "terms", "unknown"} {
		t.Run(entityType, func(t *testing.T) {
			err := cancelEntity(t, units, entityType, uuid.NewString())
			assert.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
		})
	}
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(cancelEntity(t, units, "post", " ")))
	require.NoError(t, cancelEntity(t, units, "site", og.SiteEntityID))
}

func TestCancelEntityRollsBackWhenACancellationFails(t *testing.T) {
	store, units := ogtest.New()
	entityID := uuid.NewString()
	plan := mustPlan(t, units, localeRequest("post", entityID, "KO", "ko"))
	store.Err["MarkGenerationCancelled"] = assert.AnError

	require.ErrorIs(t, cancelEntity(t, units, "post", entityID), assert.AnError)
	require.Equal(t, og.StatusQueued, generation(t, store, plan.GenerationIDs[0]).Status)
	target, _ := store.StoredTarget("post", entityID, new("ko"))
	require.NotNil(t, target.LatestGenerationID)
}
