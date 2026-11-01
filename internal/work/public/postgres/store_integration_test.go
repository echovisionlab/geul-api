//go:build integration

package workpublicpostgres_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	referencecatalogadapter "github.com/echovisionlab/geul-api/internal/adapters/referencecatalog"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/echovisionlab/geul-api/internal/work"
	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
	workpublic "github.com/echovisionlab/geul-api/internal/work/public"
	workpublicpostgres "github.com/echovisionlab/geul-api/internal/work/public/postgres"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// These tests keep the public read's own SQL: the public status gate, the
// map bounds, the Work and its relations, the share link, the statement
// budget of a list page and the snapshot a read holds while the Work is
// unpublished. The use cases above the Store run on a fake in package
// public.

const cdn = "https://cdn.example.com"

type fixture struct {
	postgres *pgtest.AppPostgres
	db       *gorm.DB
	contents *contentblock.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true})
	contents, err := contentblock.NewGeneratedStore(unusedFileReuse{})
	require.NoError(t, err)
	return &fixture{postgres: database, db: database.DB, contents: contents}
}

// runtime is the production Work runtime over db.
func runtime(db *gorm.DB) *workadapter.Runtime {
	planner := ogpostgres.NewPlanner(cdn, renderConfig{}, workadapter.NewProjection())
	return workadapter.NewRuntime(db, cdn, ogpostgres.NewRefresher(planner, ogpostgres.NewResolver(workadapter.NewRequests())))
}

func (f *fixture) units(t *testing.T, db *gorm.DB) mutationtest.Harness[workpublic.Store] {
	t.Helper()
	units, err := workpublicpostgres.NewUnits(db, workpublicpostgres.Ports{
		ContentBlocks: f.contents, MapPlaces: referencecatalogadapter.PublicMapPlaces{}, Assets: runtime(db),
	})
	require.NoError(t, err)
	return mutationtest.PostgresHarness(db, units)
}

// service serves the public reads over db; assets, when set, stands in for
// the projection's media resolution.
func (f *fixture) service(t *testing.T, db *gorm.DB, assets workpublic.MediaResolver) *workpublic.Service {
	t.Helper()
	units, err := workpublicpostgres.NewUnits(db, workpublicpostgres.Ports{
		ContentBlocks: f.contents, MapPlaces: referencecatalogadapter.PublicMapPlaces{}, Assets: runtime(db),
	})
	require.NoError(t, err)
	if assets == nil {
		assets = runtime(db)
	}
	service, err := workpublic.NewService(workpublic.Deps{
		Units: units, SpiceDB: denyAll{}, BlockMedia: passthroughMedia{}, Assets: assets,
		Members: workadapter.NewMemberSummaries(db, cdn),
	})
	require.NoError(t, err)
	return service
}

// seed stores a Work with its source title and, when body is set, one
// paragraph Block in English.
func (f *fixture) seed(t *testing.T, status work.Status, title string, change func(*work.Work), body string) work.Work {
	t.Helper()
	ctx := t.Context()
	var value work.Work
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		snapshot, err := f.contents.CreateDocument(ctx, tx, contentblock.CreateInput{Profile: "work", SourceLocale: "en"})
		if err != nil {
			return err
		}
		documentID := snapshot.Document.ID.String()
		value = work.Work{Type: "WORK_TYPE_PORTFOLIO", Year: 2026, Month: 9, IsPresent: true, SourceLocale: "en", Status: status, ContentDocumentID: &documentID}
		if status != work.StatusDraft {
			publishedAt := time.Now().UTC()
			value.PublishedAt = &publishedAt
		}
		if change != nil {
			change(&value)
		}
		store := workpostgres.Bind(tx, workpostgres.Ports{}, nil).Store
		if err := store.InsertWork(ctx, &value); err != nil {
			return err
		}
		if err := store.UpsertSourceLocale(ctx, value.ID, "en", &title, nil, time.Now()); err != nil {
			return err
		}
		if body == "" {
			return nil
		}
		input, err := contentblock.ReplaceFromRichTextProto(snapshot.Document.ID, snapshot.Document.Revision, paragraphDocument(uuid.New(), body))
		if err != nil {
			return err
		}
		_, err = f.contents.ReplaceSnapshot(ctx, tx, input, englishSource)
		return err
	}))
	return value
}

func (f *fixture) seedPlace(t *testing.T, lat, lng float64) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO map_place (id, name, address, lat, lng) VALUES (?, 'Place', 'Road', ?, ?)`, id, lat, lng).Error)
	return id
}

func TestPublicStoreListsAndMapsOnlyPublishedAndArchivedWorksIntegration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	place := f.seedPlace(t, 37.55, 126.99)
	at := func(value *work.Work) { value.MapPlaceID = &place }
	f.seed(t, work.StatusDraft, "Status Draft", at, "")
	published := f.seed(t, work.StatusPublished, "Status Published", at, "")
	archived := f.seed(t, work.StatusArchived, "Status Archived", at, "")
	f.seed(t, work.StatusPublished, "Unrelated", nil, "")
	h := f.units(t, f.db)
	search := &commonv1.FilterSpec{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "status"}
	statusIs := func(op commonv1.FilterOp, values ...string) *commonv1.FilterSpec {
		filter := &commonv1.FilterSpec{Field: "status", Op: op, Values: values}
		if op == commonv1.FilterOp_FILTER_OP_EQ || op == commonv1.FilterOp_FILTER_OP_NEQ {
			filter.Value, filter.Values = values[0], nil
		}
		return filter
	}
	bounds := workpublic.MapBounds{West: 126, South: 37, East: 127, North: 38}

	for name, test := range map[string]struct {
		filter *commonv1.FilterSpec
		want   []string
	}{
		"default":       {want: []string{archived.ID, published.ID}},
		"published":     {filter: statusIs(commonv1.FilterOp_FILTER_OP_EQ, string(work.StatusPublished)), want: []string{published.ID}},
		"archived":      {filter: statusIs(commonv1.FilterOp_FILTER_OP_EQ, string(work.StatusArchived)), want: []string{archived.ID}},
		"not published": {filter: statusIs(commonv1.FilterOp_FILTER_OP_NEQ, string(work.StatusPublished)), want: []string{archived.ID}},
		"not archived":  {filter: statusIs(commonv1.FilterOp_FILTER_OP_NOT_IN, string(work.StatusArchived)), want: []string{published.ID}},
	} {
		filters := []*commonv1.FilterSpec{search}
		if test.filter != nil {
			filters = append(filters, test.filter)
		}
		title := []*commonv1.SortSpec{{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC}}
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store workpublic.Store) error {
			listed, total, err := store.ListWorks(ctx, workpublic.ListPage{Filters: filters, Sorts: title, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, int64(len(test.want)), total, name)
			require.Equal(t, test.want, workIDs(listed), name)
			rows, err := store.MapFeatures(ctx, bounds, filters, title)
			require.NoError(t, err)
			require.Equal(t, test.want, mapWorkIDs(rows), name)
			return nil
		}))
	}

	require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store workpublic.Store) error {
		filters := []*commonv1.FilterSpec{search, statusIs(commonv1.FilterOp_FILTER_OP_EQ, string(work.StatusPublished))}
		rows, err := store.MapFeatures(ctx, bounds, filters, nil)
		require.NoError(t, err)
		require.Equal(t, []workpublic.MapFeatureRow{{
			WorkID: published.ID, WorkTitle: "Status Published", PlaceID: place, PlaceName: "Place", PlaceAddress: "Road", PlaceLat: 37.55, PlaceLng: 126.99,
		}}, rows)
		for name, test := range map[string]struct {
			bounds workpublic.MapBounds
			want   int
		}{
			"across the antimeridian": {bounds: workpublic.MapBounds{West: 126.5, South: 37, East: -170, North: 38}, want: 1},
			"every longitude":         {bounds: workpublic.MapBounds{West: 0, South: 37, East: 1, North: 38, FullLongitude: true}, want: 1},
			"elsewhere":               {bounds: workpublic.MapBounds{West: 127, South: 37, East: 128, North: 38}, want: 0},
			"another latitude":        {bounds: workpublic.MapBounds{West: 126, South: 38, East: 127, North: 39}, want: 0},
		} {
			rows, err := store.MapFeatures(ctx, test.bounds, filters, nil)
			require.NoError(t, err)
			require.Len(t, rows, test.want, name)
		}
		return nil
	}))
}

func TestPublicStoreReadsAWorkWithItsRelationsAndShareLinksIntegration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	place := f.seedPlace(t, 37.5, 127)
	slug := "public-" + uuid.NewString()
	value := f.seed(t, work.StatusPublished, "Source title", func(value *work.Work) { value.Slug, value.MapPlaceID = &slug, &place }, "Body")
	clientA, clientB, artistID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO client (id, name, website) VALUES (?, 'Client A', 'https://a.example.com'), (?, 'Client B', NULL)`, clientA, clientB).Error)
	artistDocument := uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`, artistDocument).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO artist (id, slug, content_document_id) VALUES (?, 'credited-artist', ?)`, artistID, artistDocument).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO artist_translation (entity_id, locale, title) VALUES (?, 'en', 'Credited Artist')`, artistID).Error)
	group := work.CreditGroup{WorkID: value.ID, Name: "Group"}
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		store := workpostgres.Bind(tx, workpostgres.Ports{}, nil).Store
		require.NoError(t, store.InsertTargetLocale(ctx, value.ID, work.LocaleMetadata{Locale: "ko", Title: text("번역 제목"), UpdatedAt: time.Now()}))
		require.NoError(t, store.ReplaceClients(ctx, value.ID, []string{clientB, clientA}))
		require.NoError(t, store.InsertCreditGroup(ctx, &group))
		require.NoError(t, store.InsertCredit(ctx, &work.Credit{WorkID: value.ID, GroupID: &group.ID, ArtistID: &artistID, SortOrder: 1}))
		return store.InsertCredit(ctx, &work.Credit{WorkID: value.ID, Name: text("Named"), SortOrder: 0})
	}))
	hash, err := crypto.NewPasswordHasher(nil).Hash("secret")
	require.NoError(t, err)
	live, expired := uuid.NewString(), uuid.NewString()
	for token, expiry := range map[string]time.Time{live: time.Now().Add(time.Hour), expired: time.Now().Add(-time.Minute)} {
		require.NoError(t, f.db.Exec(`INSERT INTO share_link (id, token, entity_type, entity_id, password_hash, expires_at, created_at) VALUES (?, ?, 'SHARE_LINK_ENTITY_TYPE_WORK', ?, ?, ?, ?)`,
			uuid.NewString(), token, value.ID, hash, expiry, expiry.Add(-2*time.Hour)).Error)
	}
	h := f.units(t, f.db)

	// The localization reads may store the default translation settings, so
	// they run in a writable snapshot, as the public Get does.
	require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store workpublic.Store) error {
		_, err := store.FindWork(ctx, uuid.NewString(), true)
		require.ErrorIs(t, err, work.ErrWorkNotFound)
		_, err = store.FindWork(ctx, "missing-slug", false)
		require.ErrorIs(t, err, work.ErrWorkNotFound)
		for _, found := range []func() (workpublic.Work, error){
			func() (workpublic.Work, error) { return store.FindWork(ctx, value.ID, true) },
			func() (workpublic.Work, error) { return store.FindWork(ctx, slug, false) },
		} {
			stored, err := found()
			require.NoError(t, err)
			require.Equal(t, value.ID, stored.ID)
			require.Equal(t, place, stored.MapPlace.GetId())
		}
		documentID, err := store.ContentDocumentID(ctx, value.ID)
		require.NoError(t, err)
		require.Equal(t, *value.ContentDocumentID, documentID.String())
		document, err := store.Document(ctx, value.ID, documentID, "en")
		require.NoError(t, err)
		require.Equal(t, "Body", document.Document.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
		require.NotEmpty(t, document.Revision)

		korean, err := store.ResolveLocalization(ctx, value.ID, "ko")
		require.NoError(t, err)
		require.Equal(t, "ko", korean.DisplayedLocale)
		require.Equal(t, "번역 제목", *korean.Title)
		fallback, err := store.ResolveLocalization(ctx, value.ID, "ja")
		require.NoError(t, err)
		require.Equal(t, "en", fallback.DisplayedLocale)
		batch, err := store.ResolveLocalizations(ctx, []string{value.ID}, "ko")
		require.NoError(t, err)
		require.Equal(t, "ko", batch[value.ID].DisplayedLocale)

		groups, err := store.CreditGroups(ctx, value.ID)
		require.NoError(t, err)
		require.Equal(t, []work.CreditGroup{group}, groups)
		credits, err := store.Credits(ctx, value.ID)
		require.NoError(t, err)
		require.Len(t, credits, 2)
		require.Equal(t, "Named", *credits[0].Name, "credits read by sort order")
		require.Equal(t, artistID, *credits[1].ArtistID)
		artist, found, err := store.CreditArtist(ctx, artistID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, workpublic.CreditArtistRow{ID: artistID, Name: "Credited Artist", Slug: text("credited-artist")}, artist)
		_, found, err = store.CreditArtist(ctx, uuid.NewString())
		require.NoError(t, err)
		require.False(t, found)
		clients, err := store.Clients(ctx, value.ID)
		require.NoError(t, err)
		require.Equal(t, []work.ClientRow{{ID: clientB, Name: "Client B"}, {ID: clientA, Name: "Client A", Website: text("https://a.example.com")}}, clients)

		link, found, err := store.ShareLink(ctx, live, value.ID, time.Now())
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, hash, *link.PasswordHash)
		require.NotNil(t, link.Witness)
		for name, test := range map[string]struct{ token, workID string }{
			"expired":      {expired, value.ID},
			"another work": {live, uuid.NewString()},
			"unknown":      {uuid.NewString(), value.ID},
		} {
			_, found, err := store.ShareLink(ctx, test.token, test.workID, time.Now())
			require.NoError(t, err)
			require.False(t, found, name)
		}
		return nil
	}))
}

// A list page reads its Works, their source rows and localizations in a
// fixed number of statements, whatever its size.
func TestPublicListStaysInAFixedStatementBudgetIntegration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const fixtureSize = 20
	for i := range fixtureSize {
		f.seed(t, work.StatusPublished, fmt.Sprintf("Budget %02d", i), nil, "")
	}
	counted, err := gorm.Open(postgres.Open(f.postgres.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := counted.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	recorder := &selectRecorder{}
	require.NoError(t, counted.Callback().Query().After("gorm:query").Register("record_work_list_queries", recorder.record))
	require.NoError(t, counted.Callback().Row().After("gorm:row").Register("record_work_list_rows", recorder.record))
	service := f.service(t, counted, nil)

	for _, size := range []int32{1, 6, fixtureSize} {
		recorder.reset()
		page, err := service.List(context.Background(), workpublic.ListWorksQuery{Limit: size})
		require.NoError(t, err)
		require.Len(t, page.Works, int(size))
		selects, featuredFileReads := recorder.counts()
		require.Equal(t, 7, selects, "fixed SELECT budget for a page without featured images")
		require.Zero(t, featuredFileReads, "Work.featured_image_file_id must come from the loaded page row")
	}
}

// A public read answers from one snapshot: the Work, its relations and its
// body stay as published even when an unpublish and a draft edit commit
// before the read's projection finishes. The edit waits for the read's
// share lock on the Work root.
func TestPublicReadKeepsItsSnapshotAcrossAnUnpublishIntegration(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := t.Context()
	publishedImage, publishedAsset := seedReadyImage(t, f.db)
	draftImage, _ := seedReadyImage(t, f.db)
	publishedClient, draftClient := uuid.NewString(), uuid.NewString()
	require.NoError(t, f.db.Exec(`INSERT INTO client (id, name) VALUES (?, 'Published client'), (?, 'Draft client')`, publishedClient, draftClient).Error)
	value := f.seed(t, work.StatusPublished, "Snapshot", func(value *work.Work) { value.FeaturedImageFileID = &publishedImage }, "published body")
	group := work.CreditGroup{WorkID: value.ID, Name: "Published group"}
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		store := workpostgres.Bind(tx, workpostgres.Ports{}, nil).Store
		require.NoError(t, store.ReplaceClients(ctx, value.ID, []string{publishedClient}))
		require.NoError(t, store.InsertCreditGroup(ctx, &group))
		return store.InsertCredit(ctx, &work.Credit{WorkID: value.ID, GroupID: &group.ID, Name: text("Published credit")})
	}))

	reader, err := gorm.Open(postgres.Open(f.postgres.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := reader.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	rootRead, releaseRoot := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, reader.Callback().Query().After("gorm:query").Register("pause_after_work_root", func(tx *gorm.DB) {
		if tx.Statement.Table == "work" && strings.Contains(strings.ToLower(tx.Statement.SQL.String()), `from "work"`) {
			once.Do(func() {
				close(rootRead)
				<-releaseRoot
			})
		}
	}))
	gate := &projectionGate{MediaResolver: runtime(reader), entered: make(chan struct{}), release: make(chan struct{})}
	service := f.service(t, reader, gate)

	type result struct {
		detail workpublic.Detail
		err    error
	}
	read := make(chan result, 1)
	go func() {
		detail, err := service.Get(context.Background(), workpublic.GetWorkQuery{Slug: value.ID})
		read <- result{detail, err}
	}()
	<-rootRead
	written := make(chan error, 1)
	go func() { written <- f.unpublishWithDraftEdits(ctx, value, draftImage, draftClient) }()
	pgtest.RequireLockWaiters(t, f.db, 1)
	close(releaseRoot)
	<-gate.entered
	// The draft edit commits before the projection outside the snapshot
	// runs, so a read split across snapshots would show it.
	require.NoError(t, <-written)
	close(gate.release)
	got := <-read

	require.NoError(t, got.err)
	require.Equal(t, work.StatusPublished, got.detail.Work.Status)
	require.Equal(t, "published body", got.detail.Document.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText())
	require.Equal(t, []workpublic.Client{{ID: publishedClient, Name: "Published client"}}, got.detail.Clients)
	require.Contains(t, got.detail.FeaturedImage.GetUrl(), publishedAsset)
	require.Equal(t, []work.CreditGroup{group}, got.detail.CreditGroups)
	require.Len(t, got.detail.Credits, 1)
	require.Equal(t, "Published credit", *got.detail.Credits[0].Credit.Name)
}

// unpublishWithDraftEdits returns the Work to draft and replaces its
// clients, featured image, credits and body in one transaction that first
// locks the Work root, as the manage writes do.
func (f *fixture) unpublishWithDraftEdits(ctx context.Context, value work.Work, image, client string) error {
	return f.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var root struct{ ContentDocumentID uuid.UUID }
		if err := tx.Table("work").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Select("content_document_id").Where("id = ?", value.ID).Take(&root).Error; err != nil {
			return err
		}
		store := workpostgres.Bind(tx, workpostgres.Ports{}, nil).Store
		now := time.Now()
		if err := store.SetStatus(ctx, value.ID, work.StatusDraft, nil, now); err != nil {
			return err
		}
		if err := store.ReplaceClients(ctx, value.ID, []string{client}); err != nil {
			return err
		}
		if err := store.SetFeaturedImage(ctx, value.ID, &image, now); err != nil {
			return err
		}
		group := work.CreditGroup{WorkID: value.ID, Name: "Draft group", SortOrder: 1}
		if err := store.InsertCreditGroup(ctx, &group); err != nil {
			return err
		}
		if err := store.InsertCredit(ctx, &work.Credit{WorkID: value.ID, GroupID: &group.ID, Name: text("Draft credit"), SortOrder: 1}); err != nil {
			return err
		}
		snapshot, err := f.contents.LoadSnapshotInTransaction(ctx, tx, root.ContentDocumentID, "en")
		if err != nil {
			return err
		}
		input, err := contentblock.ReplaceFromRichTextProto(root.ContentDocumentID, snapshot.Document.Revision, paragraphDocument(uuid.New(), "draft body"))
		if err != nil {
			return err
		}
		_, err = f.contents.ReplaceSnapshot(ctx, tx, input, englishSource)
		return err
	})
}

// projectionGate holds the read's projection, which runs after its
// snapshot, until released.
type projectionGate struct {
	workpublic.MediaResolver
	entered chan struct{}
	release chan struct{}
}

func (g *projectionGate) ResolveReadyOGAsset(ctx context.Context, source, localized *string) (*commonv1.AssetRef, error) {
	close(g.entered)
	<-g.release
	return g.MediaResolver.ResolveReadyOGAsset(ctx, source, localized)
}

type selectRecorder struct {
	mu                sync.Mutex
	selects           int
	featuredFileReads int
}

func (r *selectRecorder) record(tx *gorm.DB) {
	sql := strings.ToLower(strings.TrimSpace(tx.Statement.SQL.String()))
	if !strings.HasPrefix(sql, "select") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selects++
	if tx.Statement.Table == "work" && strings.Contains(sql, "featured_image_file_id as file_id") && strings.Contains(sql, "where work.id =") {
		r.featuredFileReads++
	}
}

func (r *selectRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selects, r.featuredFileReads = 0, 0
}

func (r *selectRecorder) counts() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.selects, r.featuredFileReads
}

func workIDs(works []work.Work) []string {
	ids := []string{}
	for _, value := range works {
		ids = append(ids, value.ID)
	}
	return ids
}

func mapWorkIDs(rows []workpublic.MapFeatureRow) []string {
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.WorkID)
	}
	return ids
}

func text(value string) *string { return &value }

func paragraphDocument(blockID uuid.UUID, body string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint, Profile: contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, SourceLocale: "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block:     &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{Locale: "en", Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID.String(),
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props:   &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: body}}}},
			}},
		}}}},
	}
}

func englishSource(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
	return contentblock.DomainContext{SourceLocale: "en"}, nil
}

// seedReadyImage stores a library File with a ready image asset and
// returns both IDs.
func seedReadyImage(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	require.NoError(t, db.Exec(`INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, ?, 'image/webp', 1024, 'webp', ?)`, id, id, digest[:]).Error)
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), cdn)
	asset, _, err := mediaasset.AllocationResultProto(lifecycle.AllocatePublicAsset(t.Context(), mediaasset.Allocation{
		SourceFileID: &id, Kind: "image", Extension: "webp", MimeType: "image/webp",
		Disposition: mediaasset.DispositionInline,
	}))
	require.NoError(t, err)
	assetDigest := sha256.Sum256([]byte(asset.ID))
	_, err = lifecycle.CompletePublicAsset(t.Context(), mediaasset.WrittenAssetFromProto(&commonv1.AssetWriteResult{AssetId: asset.ID, FileSize: 1024, Sha256: assetDigest[:]}))
	require.NoError(t, err)
	return id, asset.ID
}

type unusedFileReuse struct {
	contentblock.FileReuseAuthorizer
}

type passthroughMedia struct{}

func (passthroughMedia) HydrateAuthorizedContentBlockMedia(_ context.Context, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

// denyAll answers every draft view check no; these reads are public.
type denyAll struct{}

func (denyAll) Can(context.Context, policyv1.AuthorizationDecision) (bool, error) { return false, nil }

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}
