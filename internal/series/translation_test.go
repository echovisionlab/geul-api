package series_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// inTransaction runs work as the translation application does: in its own
// transaction, with the Series translation store bound to it.
func inTransaction(t *testing.T, f *fixture, work func(store series.TranslationStore) error) error {
	t.Helper()
	return mutationTransaction(t, f, func(store series.Store) error { return work(store) })
}

func (f *fixture) seedTranslatable(t *testing.T) (series.Series, *translation.ExtractionPlan) {
	t.Helper()
	root := f.store.SeedSeries(series.Series{Title: "Post Series XLIFF", Description: text("Source summary")})
	var plan *translation.ExtractionPlan
	require.NoError(t, inTransaction(t, f, func(store series.TranslationStore) error {
		source, err := series.LoadTranslationSourceDocument(t.Context(), store, root.ID)
		if err != nil {
			return err
		}
		require.Equal(t, "en", source.SourceLocale)
		require.Equal(t, f.store.Revision(root.ID), source.ContentDocumentRevision)
		require.Equal(t, text("Source summary"), source.ContentText)
		plan, err = series.BuildTranslationExtractionPlan(root.ID, "en", "ko", source)
		return err
	}))
	return root, plan
}

func interchange(root series.Series, plan *translation.ExtractionPlan, expected *string, title string) series.TranslationInterchangeMutation {
	return series.TranslationInterchangeMutation{
		SeriesID: root.ID, SourceLocale: "en", TargetLocale: "ko", Mode: series.InterchangeModePatch,
		ExpectedRevision: expected, Plan: plan,
		Targets:     map[string]translation.UnitResult{"entity:title": {UnitID: "entity:title", TranslatedText: title}},
		UnitHandles: []string{"entity:title"}, Now: contractTime,
	}
}

func TestTranslationInterchangeStoresASparseTargetAndComparesItsRevision(t *testing.T) {
	f := newFixture(t)
	root, plan := f.seedTranslatable(t)
	ctx := f.ctx(t)

	var result series.TranslationInterchangeResult
	require.NoError(t, inTransaction(t, f, func(store series.TranslationStore) error {
		var err error
		result, err = f.service.ApplyTranslationInterchange(ctx, store, interchange(root, plan, nil, ""))
		return err
	}))
	require.True(t, result.Changed)
	require.Equal(t, []string{"entity:title"}, result.AffectedUnitHandles)
	stored, ok := f.store.StoredTranslation(root.ID, "ko")
	require.True(t, ok)
	require.Equal(t, text(""), stored.Title, "an explicit empty target stays empty")
	require.Nil(t, stored.Summary, "an absent unit stays absent")
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, "ko", records[0].Locale)
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, records[0].ItemOperation)

	var loaded series.TranslationInterchangeTarget
	require.NoError(t, inTransaction(t, f, func(store series.TranslationStore) error {
		var err error
		loaded, err = f.service.LoadTranslationInterchangeTarget(ctx, store, root.ID, "ko", plan)
		return err
	}))
	require.True(t, loaded.Exists)
	require.Equal(t, result.Revision, loaded.Revision)

	_, err := f.service.UpdateSeries(ctx, series.UpdateSeriesCommand{ID: root.ID, Title: text("Source changed")})
	require.NoError(t, err)
	err = inTransaction(t, f, func(store series.TranslationStore) error {
		_, err := f.service.ApplyTranslationInterchange(ctx, store, interchange(root, plan, &result.Revision, "Changed after source edit"))
		return err
	})
	var conflict *translation.TargetRevisionConflict
	require.ErrorAs(t, err, &conflict, "a source change invalidates the target token")

	absent := ""
	err = inTransaction(t, f, func(store series.TranslationStore) error {
		_, err := f.service.ApplyTranslationInterchange(ctx, store, interchange(root, plan, &absent, "Changed"))
		return err
	})
	require.ErrorAs(t, err, &conflict)
}

func TestTranslationInterchangeFailsWithItsAuditRecord(t *testing.T) {
	f := newFixture(t)
	root, plan := f.seedTranslatable(t)
	f.audit.Err = errDown

	err := inTransaction(t, f, func(store series.TranslationStore) error {
		_, err := f.service.ApplyTranslationInterchange(f.ctx(t), store, interchange(root, plan, nil, "Titre"))
		return err
	})

	require.ErrorIs(t, err, errDown)
	_, ok := f.store.StoredTranslation(root.ID, "ko")
	require.False(t, ok, "the caller's transaction rolls the target back")
}

func TestTranslationInterchangeTargetRevisionIncludesTheDocumentRevision(t *testing.T) {
	f := newFixture(t)
	root, plan := f.seedTranslatable(t)
	f.store.SeedTranslation(series.Translation{SeriesID: root.ID, Locale: "ko", Title: text("Korean"), CreatedAt: contractTime, UpdatedAt: contractTime})
	load := func() string {
		var target series.TranslationInterchangeTarget
		require.NoError(t, inTransaction(t, f, func(store series.TranslationStore) error {
			var err error
			target, err = f.service.LoadTranslationInterchangeTarget(t.Context(), store, root.ID, "ko", plan)
			return err
		}))
		return target.Revision
	}
	before := load()
	require.NoError(t, mutationTransaction(t, f, func(store series.Store) error {
		root, err := store.Series(t.Context(), root.ID)
		if err != nil {
			return err
		}
		return store.AdvanceContentDocument(t.Context(), root.ID, root.ContentDocumentID, f.store.Revision(root.ID), uuid.NewString(), contractTime)
	}))
	require.NotEqual(t, before, load())
}

func TestRequireViewAndLockMasksTheSeries(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Translated")
	require := func(t *testing.T, seriesID string) error {
		return inTransaction(t, f, func(store series.TranslationStore) error {
			return series.RequireViewAndLock(f.ctx(t), store, func(can policyv1.Can) error {
				return identitystate.RequireFreshCan(f.ctx(t), nil, f.fence, f.checker, can)
			}, seriesID)
		})
	}

	if err := require(t, root.ID); err != nil {
		t.Fatalf("an allowed view: %v", err)
	}
	requireCode(t, connect.CodeNotFound, require(t, uuid.NewString()))
	f.checker.Deny(policyv1.PostSeries.View(root.ID))
	requireCode(t, connect.CodeNotFound, require(t, root.ID))
}
