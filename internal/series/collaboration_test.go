package series_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func (f *fixture) seedRoom(t *testing.T) (series.Series, string) {
	t.Helper()
	root := f.store.SeedSeries(series.Series{Title: "시리즈", Description: text("기존 요약")})
	f.store.SeedTranslation(series.Translation{
		SeriesID: root.ID, Locale: "ko", Title: text("시리즈"), Summary: text("기존 요약"),
		ContentText: text("기존 요약"), CreatedAt: contractTime, UpdatedAt: contractTime,
	})
	return root, uuid.NewString()
}

func TestSavePostSeriesDocumentWritesTheExactTargetRoom(t *testing.T) {
	f := newFixture(t)
	root, contributor := f.seedRoom(t)

	loaded, err := f.service.LoadPostSeriesDocument(t.Context(), series.LoadPostSeriesDocumentQuery{SeriesID: root.ID, Locale: "ko"})
	require.NoError(t, err)
	require.True(t, loaded.LocaleExists)
	require.Equal(t, "en", loaded.SourceLocale)
	require.Equal(t, text("기존 요약"), loaded.Requested.Summary)
	require.NotNil(t, loaded.TargetRevision)

	saved, err := f.service.SavePostSeriesDocument(t.Context(), series.SavePostSeriesDocumentCommand{
		SeriesID: root.ID, Locale: "ko", ExpectedDocumentRevision: loaded.DocumentRevision, ExpectedTargetRevision: loaded.TargetRevision,
		Requested: &series.LocaleFields{Title: text("시리즈"), Summary: text("새 요약")}, ContributorMemberIDs: []string{contributor},
	})

	require.NoError(t, err)
	require.Equal(t, loaded.DocumentRevision, saved.DocumentRevision, "a target save keeps the shared document revision")
	require.NotEqual(t, *loaded.TargetRevision, *saved.TargetRevision)
	stored, _ := f.store.StoredTranslation(root.ID, "ko")
	require.Equal(t, text("새 요약"), stored.ContentText)
	require.Empty(t, f.store.OGRequests(), "an unchanged title requests no OG image")
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, contributor, records[0].MemberID, "the save is attributed to its contributor")
	require.Equal(t, "ko", records[0].Locale)
	require.Equal(t, []string{"locale_content"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	require.Empty(t, f.checker.Checks(), "the contributors authorize the save, not the caller")
}

func TestSavePostSeriesDocumentAdvancesTheSourceAndRequestsItsOG(t *testing.T) {
	f := newFixture(t)
	root, contributor := f.seedRoom(t)
	loaded, err := f.service.LoadPostSeriesDocument(t.Context(), series.LoadPostSeriesDocumentQuery{SeriesID: root.ID, Locale: "en"})
	require.NoError(t, err)
	require.Nil(t, loaded.TargetRevision)

	command := series.SavePostSeriesDocumentCommand{
		SeriesID: root.ID, Locale: "en", ExpectedDocumentRevision: loaded.DocumentRevision,
		Requested: &series.LocaleFields{Title: text("  New source  ")}, ContributorMemberIDs: []string{contributor},
	}
	saved, err := f.service.SavePostSeriesDocument(t.Context(), command)

	require.NoError(t, err)
	require.NotEqual(t, loaded.DocumentRevision, saved.DocumentRevision)
	require.Equal(t, f.store.Revision(root.ID), saved.DocumentRevision)
	stored, _ := f.store.StoredTranslation(root.ID, "en")
	require.Equal(t, text("New source"), stored.Title)
	require.Equal(t, []seriestest.OGRequest{{SeriesID: root.ID, Locale: "en", Reason: "series_collaboration_title_updated"}}, f.store.OGRequests())

	command.ExpectedDocumentRevision = saved.DocumentRevision
	command.Requested = &series.LocaleFields{Title: text("New source")}
	unchanged, err := f.service.SavePostSeriesDocument(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, saved.DocumentRevision, unchanged.DocumentRevision)
	require.Len(t, f.audit.Records(), 1, "an unchanged save appends no record")
}

func TestSavePostSeriesDocumentAnswersRevisionConflicts(t *testing.T) {
	f := newFixture(t)
	root, contributor := f.seedRoom(t)
	loaded, err := f.service.LoadPostSeriesDocument(t.Context(), series.LoadPostSeriesDocumentQuery{SeriesID: root.ID, Locale: "ko"})
	require.NoError(t, err)

	_, err = f.service.SavePostSeriesDocument(t.Context(), series.SavePostSeriesDocumentCommand{
		SeriesID: root.ID, Locale: "ko", ExpectedDocumentRevision: uuid.NewString(), ExpectedTargetRevision: loaded.TargetRevision,
		Requested: &series.LocaleFields{}, ContributorMemberIDs: []string{contributor},
	})
	documentConflict := conflictDetail(t, err)
	require.Equal(t, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED, documentConflict.GetReason())
	require.Equal(t, loaded.DocumentRevision, documentConflict.GetCurrentDocumentRevision())

	stale := "stale"
	_, err = f.service.SavePostSeriesDocument(t.Context(), series.SavePostSeriesDocumentCommand{
		SeriesID: root.ID, Locale: "ko", ExpectedDocumentRevision: loaded.DocumentRevision, ExpectedTargetRevision: &stale,
		Requested: &series.LocaleFields{}, ContributorMemberIDs: []string{contributor},
	})
	targetConflict := conflictDetail(t, err)
	require.Equal(t, intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, targetConflict.GetReason())
	require.Equal(t, loaded.DocumentRevision, targetConflict.GetCurrentDocumentRevision())
	require.Empty(t, f.audit.Records())
}

// conflictDetail is the collaboration conflict a failed precondition carries.
func conflictDetail(t *testing.T, err error) *intrav1.CollaborationConflictDetail {
	t.Helper()
	requireCode(t, connect.CodeFailedPrecondition, err)
	for _, detail := range errs.Wrap(err).Details() {
		if value, valueErr := detail.Value(); valueErr == nil {
			if conflict, ok := value.(*intrav1.CollaborationConflictDetail); ok {
				return conflict
			}
		}
	}
	t.Fatalf("%v carries no collaboration conflict", err)
	return nil
}

func TestSavePostSeriesDocumentRejectsBeforeItLocks(t *testing.T) {
	f := newFixture(t)
	root, contributor := f.seedRoom(t)
	revision := uuid.NewString()
	for name, command := range map[string]series.SavePostSeriesDocumentCommand{
		"a non-canonical locale":   {SeriesID: root.ID, Locale: " ko", ExpectedDocumentRevision: revision, Requested: &series.LocaleFields{}, ContributorMemberIDs: []string{contributor}},
		"a non-canonical Series":   {SeriesID: "series", Locale: "ko", ExpectedDocumentRevision: revision, Requested: &series.LocaleFields{}, ContributorMemberIDs: []string{contributor}},
		"no requested fields":      {SeriesID: root.ID, Locale: "ko", ExpectedDocumentRevision: revision, ContributorMemberIDs: []string{contributor}},
		"two contributors":         {SeriesID: root.ID, Locale: "ko", ExpectedDocumentRevision: revision, Requested: &series.LocaleFields{}, ContributorMemberIDs: []string{contributor, uuid.NewString()}},
		"a non-canonical revision": {SeriesID: root.ID, Locale: "ko", ExpectedDocumentRevision: "revision", Requested: &series.LocaleFields{}, ContributorMemberIDs: []string{contributor}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.service.SavePostSeriesDocument(t.Context(), command)
			requireCode(t, connect.CodeInvalidArgument, err)
		})
	}
	require.Empty(t, f.units.Steps())
}

func TestSavePostSeriesDocumentKeepsTheContributorDecision(t *testing.T) {
	f := newFixture(t)
	root, contributor := f.seedRoom(t)
	loaded, err := f.service.LoadPostSeriesDocument(t.Context(), series.LoadPostSeriesDocumentQuery{SeriesID: root.ID, Locale: "en"})
	require.NoError(t, err)
	rejected := domainerr.Denied("contributor may not edit")
	f.store.RejectContributors(root.ID, rejected)

	_, err = f.service.SavePostSeriesDocument(t.Context(), series.SavePostSeriesDocumentCommand{
		SeriesID: root.ID, Locale: "en", ExpectedDocumentRevision: loaded.DocumentRevision,
		Requested: &series.LocaleFields{Title: text("Denied")}, ContributorMemberIDs: []string{contributor},
	})

	require.True(t, errors.Is(err, rejected))
	stored, _ := f.store.StoredTranslation(root.ID, "en")
	require.Equal(t, text("시리즈"), stored.Title)
}

func TestSavePostSeriesDocumentRequiresAnExistingTarget(t *testing.T) {
	f := newFixture(t)
	root, contributor := f.seedRoom(t)
	loaded, err := f.service.LoadPostSeriesDocument(t.Context(), series.LoadPostSeriesDocumentQuery{SeriesID: root.ID, Locale: "ja"})
	require.NoError(t, err)
	require.False(t, loaded.LocaleExists)
	require.Equal(t, series.LocaleFields{}, loaded.Requested)

	_, err = f.service.SavePostSeriesDocument(t.Context(), series.SavePostSeriesDocumentCommand{
		SeriesID: root.ID, Locale: "ja", ExpectedDocumentRevision: loaded.DocumentRevision,
		Requested: &series.LocaleFields{Title: text("日本語")}, ContributorMemberIDs: []string{contributor},
	})
	requireCode(t, connect.CodeFailedPrecondition, err)
}
