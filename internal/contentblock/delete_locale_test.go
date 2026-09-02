package contentblock

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStoreDeleteLocaleRemovesAggregateOverlayAndMetadataUnderOneRevision(t *testing.T) {
	db, store, seeded := newDeleteLocaleFixture(t)
	insertDeleteLocaleMetadata(t, db, seeded.DocumentID, "ko")

	var result Result
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = store.DeleteLocale(
			context.Background(),
			tx,
			DeleteLocaleInput{
				DocumentID:           seeded.DocumentID,
				ExpectedRevision:     seeded.DocumentRevision,
				Locale:               "ko",
				ContributorMemberIDs: []uuid.UUID{uuid.New()},
			},
			testFence("en"),
			deleteLocaleMetadata(seeded.DocumentID, "ko"),
		)
		return err
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, result.ContentChanged)
	require.True(t, result.MetadataChanged)
	require.False(t, result.TranslationSourceChanged)
	require.Equal(t, []string{"ko"}, result.ChangedLocales)
	require.NotEqual(t, seeded.DocumentRevision, result.DocumentRevision)
	loaded, err := store.LoadSnapshot(context.Background(), db, seeded.DocumentID, "en")
	require.NoError(t, err)
	require.Equal(t, []string{"en"}, snapshotLocales(loaded))
	requireDeleteLocaleRowCount(t, db, seeded.DocumentID, "ko", 0)
	requireDeleteLocaleMetadataCount(t, db, seeded.DocumentID, "ko", 0)
}

func TestStoreDeleteLocaleDistinguishesMetadataOnlyAndMissingNoOp(t *testing.T) {
	db, store, seeded := newDeleteLocaleFixture(t)
	insertDeleteLocaleMetadata(t, db, seeded.DocumentID, "ja")

	metadataOnly := deleteLocaleInTransaction(
		t,
		db,
		store,
		DeleteLocaleInput{
			DocumentID:       seeded.DocumentID,
			ExpectedRevision: seeded.DocumentRevision,
			Locale:           "ja",
		},
		deleteLocaleMetadata(seeded.DocumentID, "ja"),
	)
	require.True(t, metadataOnly.Changed)
	require.False(t, metadataOnly.ContentChanged)
	require.True(t, metadataOnly.MetadataChanged)
	require.Equal(t, []string{"ja"}, metadataOnly.ChangedLocales)
	require.NotEqual(t, seeded.DocumentRevision, metadataOnly.DocumentRevision)

	missing := deleteLocaleInTransaction(
		t,
		db,
		store,
		DeleteLocaleInput{
			DocumentID:       seeded.DocumentID,
			ExpectedRevision: metadataOnly.DocumentRevision,
			Locale:           "ja",
		},
		deleteLocaleMetadata(seeded.DocumentID, "ja"),
	)
	require.False(t, missing.Changed)
	require.False(t, missing.ContentChanged)
	require.False(t, missing.MetadataChanged)
	require.Empty(t, missing.ChangedLocales)
	require.Equal(t, metadataOnly.DocumentRevision, missing.DocumentRevision)
}

func TestStoreDeleteLocaleRejectsStaleSourceAndNonCanonicalContributorBeforeCallback(t *testing.T) {
	db, store, seeded := newDeleteLocaleFixture(t)
	callbackCalls := 0
	callback := func(context.Context, *gorm.DB) (bool, error) {
		callbackCalls++
		return false, nil
	}

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := store.DeleteLocale(context.Background(), tx, DeleteLocaleInput{
			DocumentID:       seeded.DocumentID,
			ExpectedRevision: uuid.New(),
			Locale:           "ko",
		}, testFence("en"), callback)
		return err
	})
	require.ErrorIs(t, err, ErrStaleRevision)

	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := store.DeleteLocale(context.Background(), tx, DeleteLocaleInput{
			DocumentID:       seeded.DocumentID,
			ExpectedRevision: seeded.DocumentRevision,
			Locale:           "en",
		}, testFence("en"), callback)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidMutation)

	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := store.DeleteLocale(context.Background(), tx, DeleteLocaleInput{
			DocumentID:       seeded.DocumentID,
			ExpectedRevision: seeded.DocumentRevision,
			Locale:           " ko ",
		}, testFence("en"), callback)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidMutation)

	first, second := uuid.New(), uuid.New()
	contributors := []uuid.UUID{first, second}
	sort.Slice(contributors, func(i, j int) bool { return contributors[i].String() > contributors[j].String() })
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := store.DeleteLocale(context.Background(), tx, DeleteLocaleInput{
			DocumentID:           seeded.DocumentID,
			ExpectedRevision:     seeded.DocumentRevision,
			Locale:               "ko",
			ContributorMemberIDs: contributors,
		}, testFence("en"), callback)
		return err
	})
	require.ErrorIs(t, err, ErrInvalidMutation)
	require.Zero(t, callbackCalls)
}

func TestStoreDeleteLocaleRollsBackOverlayAndMetadataWithOwningTransaction(t *testing.T) {
	db, store, seeded := newDeleteLocaleFixture(t)
	insertDeleteLocaleMetadata(t, db, seeded.DocumentID, "ko")
	rollback := errors.New("rollback locale delete")

	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := store.DeleteLocale(
			context.Background(),
			tx,
			DeleteLocaleInput{
				DocumentID:       seeded.DocumentID,
				ExpectedRevision: seeded.DocumentRevision,
				Locale:           "ko",
			},
			testFence("en"),
			deleteLocaleMetadata(seeded.DocumentID, "ko"),
		)
		if err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	requireDeleteLocaleRowCount(t, db, seeded.DocumentID, "ko", 2)
	requireDeleteLocaleMetadataCount(t, db, seeded.DocumentID, "ko", 1)
	loaded, err := store.LoadSnapshot(context.Background(), db, seeded.DocumentID, "en")
	require.NoError(t, err)
	require.Equal(t, seeded.DocumentRevision, loaded.Document.Revision)
	require.Equal(t, []string{"en", "ko"}, snapshotLocales(loaded))
}

func TestStoreDeleteLocaleConcurrentSameRevisionAcceptsOnceAndRejectsStale(t *testing.T) {
	db, store, seeded := newDeleteLocaleFixture(t)
	insertDeleteLocaleMetadata(t, db, seeded.DocumentID, "ko")
	input := DeleteLocaleInput{
		DocumentID:       seeded.DocumentID,
		ExpectedRevision: seeded.DocumentRevision,
		Locale:           "ko",
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var serialize sync.Mutex
	var callbackCalls atomic.Int32
	for range 2 {
		go func() {
			<-start
			serialize.Lock()
			defer serialize.Unlock()
			results <- db.Transaction(func(tx *gorm.DB) error {
				_, err := store.DeleteLocale(
					context.Background(),
					tx,
					input,
					testFence("en"),
					func(ctx context.Context, tx *gorm.DB) (bool, error) {
						callbackCalls.Add(1)
						return deleteLocaleMetadata(input.DocumentID, input.Locale)(ctx, tx)
					},
				)
				return err
			})
		}()
	}
	close(start)
	firstErr, secondErr := <-results, <-results
	require.True(t,
		(firstErr == nil && errors.Is(secondErr, ErrStaleRevision)) ||
			(secondErr == nil && errors.Is(firstErr, ErrStaleRevision)),
		"results were %v and %v", firstErr, secondErr,
	)
	require.EqualValues(t, 1, callbackCalls.Load())
	requireDeleteLocaleRowCount(t, db, seeded.DocumentID, "ko", 0)
}

type deleteLocaleFixture struct {
	Result
	DocumentID uuid.UUID
}

func newDeleteLocaleFixture(t *testing.T) (*gorm.DB, *Store, deleteLocaleFixture) {
	t.Helper()
	db, store, _ := newTestStore(t)
	require.NoError(t, db.Exec(`
		CREATE TABLE test_content_locale_metadata (
			document_id TEXT NOT NULL,
			locale TEXT NOT NULL,
			PRIMARY KEY (document_id, locale)
		)`).Error)
	created := createTestDocument(t, db, store)
	firstID, secondID := uuid.New(), uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{
			paragraph(firstID, 0, "wide"),
			paragraph(secondID, 1, "wide"),
		},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{firstID: "one", secondID: "two"}),
			localeGroup("ko", map[uuid.UUID]string{firstID: "하나", secondID: "둘"}),
		},
	})
	require.NoError(t, err)
	return db, store, deleteLocaleFixture{Result: seeded, DocumentID: created.Document.ID}
}

func deleteLocaleInTransaction(
	t *testing.T,
	db *gorm.DB,
	store *Store,
	input DeleteLocaleInput,
	metadata LocaleMetadataDeletion,
) Result {
	t.Helper()
	var result Result
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = store.DeleteLocale(context.Background(), tx, input, testFence("en"), metadata)
		return err
	})
	require.NoError(t, err)
	return result
}

func deleteLocaleMetadata(documentID uuid.UUID, locale string) LocaleMetadataDeletion {
	return func(ctx context.Context, tx *gorm.DB) (bool, error) {
		result := tx.WithContext(ctx).Exec(
			"DELETE FROM test_content_locale_metadata WHERE document_id = ? AND locale = ?",
			documentID.String(),
			locale,
		)
		return result.RowsAffected > 0, result.Error
	}
}

func insertDeleteLocaleMetadata(t *testing.T, db *gorm.DB, documentID uuid.UUID, locale string) {
	t.Helper()
	require.NoError(t, db.Exec(
		"INSERT INTO test_content_locale_metadata (document_id, locale) VALUES (?, ?)",
		documentID.String(),
		locale,
	).Error)
}

func requireDeleteLocaleRowCount(t *testing.T, db *gorm.DB, documentID uuid.UUID, locale string, expected int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM content_block_locale AS locale_row
		JOIN content_block AS block ON block.id = locale_row.block_id
		WHERE block.document_id = ? AND locale_row.locale = ?`, documentID, locale).Scan(&count).Error)
	require.Equal(t, expected, count)
}

func requireDeleteLocaleMetadataCount(t *testing.T, db *gorm.DB, documentID uuid.UUID, locale string, expected int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(
		"SELECT COUNT(*) FROM test_content_locale_metadata WHERE document_id = ? AND locale = ?",
		documentID.String(),
		locale,
	).Scan(&count).Error)
	require.Equal(t, expected, count)
}

func snapshotLocales(snapshot Snapshot) []string {
	locales := make([]string, 0, len(snapshot.LocaleOverlays))
	for _, overlay := range snapshot.LocaleOverlays {
		locales = append(locales, overlay.Locale)
	}
	return locales
}
