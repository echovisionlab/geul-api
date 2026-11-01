package series_test

import (
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateSeriesStoresADraftWithItsPolicyAndSourceCopy(t *testing.T) {
	f := newFixture(t)
	description := "About"

	created, err := f.service.CreateSeries(f.ctx(t), series.CreateSeriesCommand{
		Title: "  First Series  ", Description: &description, AcceptLanguage: "ko-KR,ko;q=0.9",
	})

	require.NoError(t, err)
	require.Equal(t, "first-series", created.Series.Slug)
	require.Equal(t, series.StatusDraft, created.Series.Status)
	require.Equal(t, "First Series", created.Series.Title)
	require.Equal(t, &description, created.Series.Description)
	require.Equal(t, "ko", created.Series.SourceLocale, "Accept-Language picks the locale when the creator has none")
	stored, ok := f.store.StoredTranslation(created.Series.ID, "ko")
	require.True(t, ok)
	require.Equal(t, text("First Series"), stored.Title)
	require.Equal(t, &description, stored.ContentText)
	require.NotEmpty(t, f.store.Revision(created.Series.ID))
	require.Equal(t, []seriestest.OGRequest{{SeriesID: created.Series.ID, Locale: "ko", Reason: "series_created"}}, f.store.OGRequests())
	touch := must(policyv1.PostSeries.TouchPolicy(created.Series.ID))
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, f.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditPostSeriesCreated}, f.actions())
	require.Equal(t, []mutation.Step{
		mutation.StepAuthorizationLock, mutation.StepAccess, mutation.StepChange, mutation.StepAudit,
		mutation.StepRelationships, mutation.StepCommit,
	}, f.units.Steps())
	require.Equal(t, []authztest.Check{authztest.Of(f.identityID, must(policyv1.PostSeries.Create()), nil)}, f.checker.Checks())
}

func TestCreateSeriesPicksTheSourceLocale(t *testing.T) {
	for name, test := range map[string]struct {
		preferred      *string
		acceptLanguage string
		lookupErr      error
		want           string
	}{
		"the creator's preference wins":             {preferred: text("ja"), acceptLanguage: "ko", want: "ja"},
		"Accept-Language follows":                   {acceptLanguage: "de-DE", want: "de"},
		"the runtime default comes last":            {want: "en"},
		"a failed lookup falls through to the next": {lookupErr: errDown, acceptLanguage: "fr", want: "fr"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.locales.preferred, f.locales.err = test.preferred, test.lookupErr
			created, err := f.service.CreateSeries(f.ctx(t), series.CreateSeriesCommand{Title: "Locale", AcceptLanguage: test.acceptLanguage})
			require.NoError(t, err)
			require.Equal(t, test.want, created.Series.SourceLocale)
		})
	}
}

func TestCreateSeriesRejectsBeforeItWrites(t *testing.T) {
	for name, test := range map[string]struct {
		command series.CreateSeriesCommand
		prepare func(f *fixture)
		want    connect.Code
	}{
		"a blank title":           {command: series.CreateSeriesCommand{Title: "  "}, want: connect.CodeInvalidArgument},
		"a slug with a separator": {command: series.CreateSeriesCommand{Title: "T", Slug: text("a/b")}, want: connect.CodeInvalidArgument},
		"a taken slug": {command: series.CreateSeriesCommand{Title: "Taken"}, want: connect.CodeAlreadyExists, prepare: func(f *fixture) {
			f.store.SeedSeries(series.Series{Slug: "taken"})
		}},
		"a Page on the route": {command: series.CreateSeriesCommand{Title: "Routed"}, want: connect.CodeAlreadyExists, prepare: func(f *fixture) {
			f.store.OccupyRoute("routed")
		}},
		"a caller who may not create": {command: series.CreateSeriesCommand{Title: "Denied"}, want: connect.CodePermissionDenied, prepare: func(f *fixture) {
			f.checker.Deny(policyv1.PostSeries.Create())
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if test.prepare != nil {
				test.prepare(f)
			}
			documents := f.store.Documents()
			_, err := f.service.CreateSeries(f.ctx(t), test.command)
			requireCode(t, test.want, err)
			require.Equal(t, documents, f.store.Documents())
			require.Empty(t, f.checker.Applied())
			require.Empty(t, f.audit.Records())
		})
	}
}

func TestCreateSeriesRollsBackWhenAuditOrTheRelationshipFails(t *testing.T) {
	t.Run("audit", func(t *testing.T) {
		f := newFixture(t)
		f.audit.Err = errDown
		_, err := f.service.CreateSeries(f.ctx(t), series.CreateSeriesCommand{Title: "Audit down"})
		require.ErrorIs(t, err, errDown)
		require.Equal(t, mutation.StepRollback, f.lastStep())
		require.Zero(t, f.store.Documents())
		require.Empty(t, f.store.OGRequests())
		require.Empty(t, f.checker.Applied(), "the relationship is written only after the audit record")
	})
	t.Run("relationship", func(t *testing.T) {
		f := newFixture(t)
		f.checker.FailWrites(errDown)
		_, err := f.service.CreateSeries(f.ctx(t), series.CreateSeriesCommand{Title: "SpiceDB down"})
		require.ErrorIs(t, err, errDown)
		require.Equal(t, mutation.StepRollback, f.lastStep())
		require.Zero(t, f.store.Documents())
	})
}

func TestUpdateSeriesValidatesBeforeItLocks(t *testing.T) {
	for name, command := range map[string]series.UpdateSeriesCommand{
		"an archived status": {Status: text("SERIES_STATUS_ARCHIVED")},
		"a blank slug":       {Slug: text("   ")},
		"an unsafe slug":     {Slug: text("series/unsafe")},
		"a long slug":        {Slug: text(strings.Repeat("s", 256))},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			root := f.seedSeries("Validated")
			command.ID = root.ID
			_, err := f.service.UpdateSeries(f.ctx(t), command)
			requireCode(t, connect.CodeInvalidArgument, err)
			require.Empty(t, f.units.Steps())
		})
	}
}

func TestUpdateSeriesPublishesRenamesAndRetitles(t *testing.T) {
	f := newFixture(t)
	root := f.store.SeedSeries(series.Series{Title: "Before", Slug: "before"})
	revision := f.store.Revision(root.ID)

	updated, err := f.service.UpdateSeries(f.ctx(t), series.UpdateSeriesCommand{
		ID: root.ID, Slug: text("  after  "), Status: text(string(series.StatusPublished)), Title: text(" After "),
	})

	require.NoError(t, err)
	require.Equal(t, "after", updated.Series.Slug)
	require.Equal(t, series.StatusPublished, updated.Series.Status)
	require.Equal(t, "After", updated.Series.Title)
	require.NotEqual(t, revision, f.store.Revision(root.ID))
	require.Equal(t, []seriestest.MenuChange{{SeriesID: root.ID, OldSlug: "before", NewSlug: "after"}}, f.store.MenuChanges())
	require.Equal(t, []seriestest.OGRequest{{SeriesID: root.ID, Locale: "en", Reason: "series_title_updated"}}, f.store.OGRequests())
	records := f.audit.Records()
	require.Len(t, records, 2)
	require.Equal(t, []string{"slug", "source_copy"}, records[0].ChangedFields)
	require.Equal(t, sharedtelemetry.AuditStateDraft, records[1].PreviousState)
	require.Equal(t, sharedtelemetry.AuditStatePublished, records[1].NewState)
	require.Equal(t, []authztest.Check{authztest.Of(f.identityID, must(policyv1.PostSeries.Publish(root.ID)), nil)}, f.checker.Checks(),
		"a status change needs publish, the one exact action")
}

func TestUpdateSeriesRewritesMenuTargetsFromTheLockedSlug(t *testing.T) {
	f := newFixture(t)
	root := f.store.SeedSeries(series.Series{Slug: "original"})
	// A writer that committed before this update's lock renamed the Series.
	require.NoError(t, mutationTransaction(t, f, func(store series.Store) error {
		return store.UpdateSeriesRoot(t.Context(), root.ID, text("intermediate"), nil, contractTime)
	}))

	_, err := f.service.UpdateSeries(f.ctx(t), series.UpdateSeriesCommand{ID: root.ID, Slug: text("final")})

	require.NoError(t, err)
	require.Equal(t, []seriestest.MenuChange{{SeriesID: root.ID, OldSlug: "intermediate", NewSlug: "final"}}, f.store.MenuChanges())
}

func TestUpdateSeriesWithoutAChangeAppendsNoRecord(t *testing.T) {
	f := newFixture(t)
	root := f.store.SeedSeries(series.Series{Title: "Same", Slug: "same"})
	revision := f.store.Revision(root.ID)

	_, err := f.service.UpdateSeries(f.ctx(t), series.UpdateSeriesCommand{ID: root.ID, Slug: text("same"), Title: text("Same")})

	require.NoError(t, err)
	require.Empty(t, f.audit.Records())
	require.Empty(t, f.store.MenuChanges())
	require.Empty(t, f.store.OGRequests())
	require.Equal(t, revision, f.store.Revision(root.ID))
}

func TestUpdateSeriesHidesTheSeriesFromACallerWithoutTheAction(t *testing.T) {
	for name, prepare := range map[string]func(f *fixture, seriesID string){
		"denied":   func(f *fixture, seriesID string) { f.checker.Deny(policyv1.PostSeries.Edit(seriesID)) },
		"inactive": func(f *fixture, _ string) { f.fence.Deactivate(f.identityID) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			root := f.seedSeries("Hidden")
			prepare(f, root.ID)
			_, err := f.service.UpdateSeries(f.ctx(t), series.UpdateSeriesCommand{ID: root.ID, Title: text("Changed")})
			requireCode(t, connect.CodeNotFound, err)
			require.Equal(t, "series not found: "+root.ID, errs.Wrap(err).Message())
			stored, _ := f.store.StoredTranslation(root.ID, "en")
			require.Equal(t, text("Hidden"), stored.Title)
		})
	}
	t.Run("missing", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.service.UpdateSeries(f.ctx(t), series.UpdateSeriesCommand{ID: uuid.NewString(), Title: text("Changed")})
		requireCode(t, connect.CodeNotFound, err)
		require.Empty(t, f.checker.Checks(), "a missing root answers before authorization")
	})
}

func TestUpdateSeriesAnswersATakenSlug(t *testing.T) {
	f := newFixture(t)
	f.store.SeedSeries(series.Series{Slug: "taken"})
	root := f.seedSeries("Mine")
	_, err := f.service.UpdateSeries(f.ctx(t), series.UpdateSeriesCommand{ID: root.ID, Slug: text("taken")})
	requireCode(t, connect.CodeAlreadyExists, err)
}

func TestDeleteSeriesClearsItsPostsBindingsAndRelationships(t *testing.T) {
	f := newFixture(t)
	root := f.store.SeedSeries(series.Series{Slug: "doomed"})
	first := f.store.SeedPost(seriestest.Post{SeriesID: &root.ID, SeriesPost: series.SeriesPost{SeriesOrder: intPointer(0)}})
	second := f.store.SeedPost(seriestest.Post{SeriesID: &root.ID, SeriesPost: series.SeriesPost{SeriesOrder: intPointer(1)}})
	f.store.SeedManager(root.ID, f.memberID, contractTime)
	policy := must(policyv1.PostSeries.TouchPolicy(root.ID))
	f.spiceDB.Relationships[root.ID] = []policyv1.RelationshipMutation{policy}

	require.NoError(t, f.service.DeleteSeries(f.ctx(t), series.DeleteSeriesCommand{ID: root.ID}))

	_, exists := f.store.Stored(root.ID)
	require.False(t, exists)
	require.Nil(t, f.store.StoredPost(first.ID).SeriesID)
	require.Nil(t, f.store.StoredPost(second.ID).SeriesID)
	require.False(t, f.store.Managed(root.ID, f.memberID))
	require.True(t, f.store.OGReleased(root.ID))
	require.Zero(t, f.store.Documents())
	require.Equal(t, []seriestest.MenuChange{{SeriesID: root.ID, OldSlug: "doomed"}}, f.store.MenuChanges())
	require.Equal(t, [][]policyv1.RelationshipMutation{{must(policyv1.PostSeries.DeletePolicy(root.ID))}}, f.checker.Applied())
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditPostSeriesDeleted}, f.actions())
}

func TestDeleteSeriesRollsBackEverythingWhenAStepFails(t *testing.T) {
	for name, test := range map[string]struct {
		prepare func(f *fixture, seriesID string)
		want    connect.Code
	}{
		"the audit record":  {prepare: func(f *fixture, _ string) { f.audit.Err = errDown }, want: connect.CodeInternal},
		"the relationships": {prepare: func(f *fixture, _ string) { f.checker.FailWrites(errDown) }, want: connect.CodeInternal},
		"the snapshot":      {prepare: func(f *fixture, _ string) { f.spiceDB.SnapshotErr = errDown }, want: connect.CodeUnavailable},
		"an empty snapshot": {prepare: func(f *fixture, seriesID string) { delete(f.spiceDB.Relationships, seriesID) }, want: connect.CodeInternal},
		"a caller who may not": {prepare: func(f *fixture, seriesID string) {
			f.checker.Deny(policyv1.PostSeries.Delete(seriesID))
		}, want: connect.CodeNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			root := f.seedSeries("Kept")
			post := f.store.SeedPost(seriestest.Post{SeriesID: &root.ID, SeriesPost: series.SeriesPost{SeriesOrder: intPointer(0)}})
			f.spiceDB.Relationships[root.ID] = []policyv1.RelationshipMutation{must(policyv1.PostSeries.TouchPolicy(root.ID))}
			test.prepare(f, root.ID)

			err := f.service.DeleteSeries(f.ctx(t), series.DeleteSeriesCommand{ID: root.ID})

			requireCode(t, test.want, err)
			require.Equal(t, mutation.StepRollback, f.lastStep())
			_, exists := f.store.Stored(root.ID)
			require.True(t, exists)
			require.Equal(t, &root.ID, f.store.StoredPost(post.ID).SeriesID)
			require.Equal(t, 1, f.store.Documents())
		})
	}
}

func TestSetSeriesFeaturedImageBindsOnceAndRequestsEveryLocale(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Imaged")
	fileID := uuid.NewString()
	image := &series.Asset{ID: "image-asset", URL: "https://cdn.example/image.webp"}
	f.store.SeedFile(fileID, image)

	changed, err := f.service.SetSeriesFeaturedImage(f.ctx(t), series.SetSeriesFeaturedImageCommand{SeriesID: root.ID, FileID: fileID})

	require.NoError(t, err)
	require.Equal(t, image, changed.Asset)
	require.Equal(t, text("run-series_featured_image_updated"), changed.OgGenerationRunID)
	bound, ok := f.store.FeaturedBinding(root.ID)
	require.True(t, ok)
	require.Equal(t, fileID, bound)
	require.Equal(t, []seriestest.OGRequest{{SeriesID: root.ID, AllLocales: true, Reason: "series_featured_image_updated"}}, f.store.OGRequests())
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationAdded, records[0].CollectionOperation)
	require.Equal(t, fileID, records[0].FileID)

	again, err := f.service.SetSeriesFeaturedImage(f.ctx(t), series.SetSeriesFeaturedImageCommand{SeriesID: root.ID, FileID: fileID})
	require.NoError(t, err)
	require.Equal(t, series.FeaturedImageChange{}, again, "the same File is a no-op")
	require.Len(t, f.store.OGRequests(), 1)
	require.Len(t, f.audit.Records(), 1)

	_, err = f.service.SetSeriesFeaturedImage(f.ctx(t), series.SetSeriesFeaturedImageCommand{SeriesID: root.ID, FileID: uuid.NewString()})
	requireCode(t, connect.CodeNotFound, err)
}

func TestDeleteSeriesFeaturedImageReleasesTheBinding(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Imaged")
	fileID := uuid.NewString()
	f.store.SeedFile(fileID, nil)

	runID, err := f.service.DeleteSeriesFeaturedImage(f.ctx(t), series.DeleteSeriesFeaturedImageCommand{SeriesID: root.ID})
	require.NoError(t, err)
	require.Nil(t, runID, "a Series without a featured image is a no-op")
	require.Empty(t, f.audit.Records())

	_, err = f.service.SetSeriesFeaturedImage(f.ctx(t), series.SetSeriesFeaturedImageCommand{SeriesID: root.ID, FileID: fileID})
	require.NoError(t, err)
	runID, err = f.service.DeleteSeriesFeaturedImage(f.ctx(t), series.DeleteSeriesFeaturedImageCommand{SeriesID: root.ID})
	require.NoError(t, err)
	require.Equal(t, text("run-series_featured_image_removed"), runID)
	_, bound := f.store.FeaturedBinding(root.ID)
	require.False(t, bound)
	stored, _ := f.store.Stored(root.ID)
	require.Nil(t, stored.FeaturedImageFileID)
	records := f.audit.Records()
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, records[len(records)-1].CollectionOperation)
	require.Equal(t, fileID, records[len(records)-1].FileID)
}

func TestListSeriesAdminProjectsAssetsAndPages(t *testing.T) {
	f := newFixture(t)
	fileID := uuid.NewString()
	ogAssetID := "og-asset"
	first := f.store.SeedSeries(series.Series{Title: "Alpha", OgAssetID: &ogAssetID, FeaturedImageFileID: &fileID, Status: series.StatusPublished})
	f.store.SeedSeries(series.Series{Title: "Beta"})
	f.media.assets[ogAssetID] = &series.Asset{ID: ogAssetID}
	f.media.images[fileID] = &series.Asset{ID: "featured"}

	page, err := f.service.ListSeriesAdmin(f.ctx(t), series.ListSeriesAdminQuery{
		Page:    series.Page{Limit: 1},
		Filters: []*commonv1.FilterSpec{{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(series.StatusPublished)}},
	})

	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, first.ID, page.Items[0].Series.ID)
	require.Equal(t, "Alpha", page.Items[0].Series.Title)
	require.Equal(t, "og-asset", page.Items[0].OgAsset.ID)
	require.Equal(t, "featured", page.Items[0].FeaturedImageAsset.ID)
	require.Equal(t, series.SeriesPage{Items: page.Items, Total: 1, Limit: 1}, page)

	all, err := f.service.ListSeriesAdmin(f.ctx(t), series.ListSeriesAdminQuery{Page: series.Page{Limit: 1}})
	require.NoError(t, err)
	require.True(t, all.HasMore)
	require.EqualValues(t, 2, all.Total)

	f.checker.Deny(policyv1.PostSeries.List())
	_, err = f.service.ListSeriesAdmin(f.ctx(t), series.ListSeriesAdminQuery{})
	requireCode(t, connect.CodePermissionDenied, err)
}

func TestListMySeriesListsWhatThePrincipalMayView(t *testing.T) {
	f := newFixture(t)
	empty, err := f.service.ListMySeries(f.ctx(t), series.ListMySeriesQuery{Page: series.Page{Limit: 5, Offset: 3}})
	require.NoError(t, err)
	require.Equal(t, series.SeriesPage{Items: []series.ListedSeries{}, Limit: 20}, empty, "an empty lookup answers the default page")

	visible := f.seedSeries("Visible")
	f.seedSeries("Hidden")
	f.spiceDB.Lookup = func(lookup policyv1.ResourceLookup, actor policyv1.Actor) ([]string, error) {
		return []string{visible.ID}, nil
	}
	page, err := f.service.ListMySeries(f.ctx(t), series.ListMySeriesQuery{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "Visible", page.Items[0].Series.Title)

	f.spiceDB.Lookup = func(policyv1.ResourceLookup, policyv1.Actor) ([]string, error) { return nil, errDown }
	_, err = f.service.ListMySeries(f.ctx(t), series.ListMySeriesQuery{})
	requireCode(t, connect.CodeUnavailable, err)
}

func TestListSeriesSimpleListsEverySeriesByTitle(t *testing.T) {
	f := newFixture(t)
	const count = 105
	ids := make([]string, 0, count)
	for index := range count {
		ids = append(ids, f.seedSeries(fmt.Sprintf("Simple %03d", count-index)).ID)
	}
	f.spiceDB.Lookup = func(policyv1.ResourceLookup, policyv1.Actor) ([]string, error) { return ids, nil }

	listed, err := f.service.ListSeriesSimple(f.ctx(t), series.ListSeriesSimpleQuery{})

	require.NoError(t, err)
	require.Len(t, listed, count, "the picker is not paged")
	require.Equal(t, "Simple 001", listed[0].Title)
	require.Equal(t, fmt.Sprintf("Simple %03d", count), listed[count-1].Title)
}

func TestCheckSeriesSlugAvailable(t *testing.T) {
	f := newFixture(t)
	mine := f.store.SeedSeries(series.Series{Slug: "mine"})
	f.store.OccupyRoute("routed")
	for name, test := range map[string]struct {
		query series.CheckSeriesSlugAvailableQuery
		want  bool
	}{
		"a free slug":          {query: series.CheckSeriesSlugAvailableQuery{Slug: "free"}, want: true},
		"a taken slug":         {query: series.CheckSeriesSlugAvailableQuery{Slug: "mine"}},
		"the Series' own slug": {query: series.CheckSeriesSlugAvailableQuery{Slug: "mine", ExcludeID: &mine.ID}, want: true},
		"a Page route":         {query: series.CheckSeriesSlugAvailableQuery{Slug: "routed"}},
	} {
		t.Run(name, func(t *testing.T) {
			available, err := f.service.CheckSeriesSlugAvailable(f.ctx(t), test.query)
			require.NoError(t, err)
			require.Equal(t, test.want, available)
		})
	}
	_, err := f.service.CheckSeriesSlugAvailable(f.ctx(t), series.CheckSeriesSlugAvailableQuery{Slug: " "})
	requireCode(t, connect.CodeInvalidArgument, err)
	f.checker.Deny(policyv1.PostSeries.Edit(mine.ID))
	_, err = f.service.CheckSeriesSlugAvailable(f.ctx(t), series.CheckSeriesSlugAvailableQuery{Slug: "free", ExcludeID: &mine.ID})
	requireCode(t, connect.CodeNotFound, err)
}

func TestSeriesReadsHideTheSeriesFromAnOutsider(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Private")
	f.checker.Deny(policyv1.PostSeries.View(root.ID))

	_, err := f.service.GetSeriesWithManagers(f.ctx(t), series.GetSeriesWithManagersQuery{ID: root.ID})
	requireCode(t, connect.CodeNotFound, err)
	_, err = f.service.ListSeriesManagers(f.ctx(t), series.ListSeriesManagersQuery{SeriesID: root.ID})
	requireCode(t, connect.CodeNotFound, err)
	_, err = f.service.ListSeriesPosts(f.ctx(t), series.ListSeriesPostsQuery{SeriesID: root.ID})
	requireCode(t, connect.CodeNotFound, err)

	g := newFixture(t)
	_, err = g.service.ListSeriesManagers(g.ctx(t), series.ListSeriesManagersQuery{SeriesID: uuid.NewString()})
	requireCode(t, connect.CodeNotFound, err)
}

func TestGetSeriesWithManagersOrdersByAttributionAndKeepsItsTime(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Managed")
	first, second := uuid.NewString(), uuid.NewString()
	f.store.SeedManager(root.ID, second, contractTime)
	f.store.SeedManager(root.ID, first, contractTime.Add(1))
	f.members[first] = series.ManagerSummary{MemberID: first, Nickname: "First"}
	f.members[second] = series.ManagerSummary{MemberID: second, Nickname: "Second", Avatar: &series.Asset{ID: "avatar"}}

	found, err := f.service.GetSeriesWithManagers(f.ctx(t), series.GetSeriesWithManagersQuery{ID: root.ID})

	require.NoError(t, err)
	require.Equal(t, "Managed", found.Series.Title)
	require.Equal(t, []series.Manager{
		{MemberID: second, Nickname: "Second", Avatar: &series.Asset{ID: "avatar"}, CreatedAt: contractTime},
		{MemberID: first, Nickname: "First", CreatedAt: contractTime.Add(1)},
	}, found.Managers)

	delete(f.members, first)
	_, err = f.service.ListSeriesManagers(f.ctx(t), series.ListSeriesManagersQuery{SeriesID: root.ID})
	require.EqualError(t, err, "series manager member was not found")
}

func TestSeriesDetailServesWithoutAFeaturedImageThatFailsToResolve(t *testing.T) {
	f := newFixture(t)
	fileID := uuid.NewString()
	root := f.store.SeedSeries(series.Series{Title: "Imaged", FeaturedImageFileID: &fileID})
	f.media.err = errDown

	found, err := f.service.GetSeriesWithManagers(f.ctx(t), series.GetSeriesWithManagersQuery{ID: root.ID})

	require.NoError(t, err)
	require.Nil(t, found.FeaturedImageAsset)
}

func mutationTransaction(t *testing.T, f *fixture, work func(series.Store) error) error {
	t.Helper()
	return f.units.Transaction(t.Context(), false, func(tx mutation.Tx[series.Store]) error { return work(tx.Store()) })
}

func intPointer(value int) *int { return &value }
