package contentblock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testContract struct{}

type layoutSensitiveLocaleContract struct{ testContract }

func (layoutSensitiveLocaleContract) ValidateBlock(
	profile string,
	block FullBlock,
) (ValidatedPayload, error) {
	validated, err := (testContract{}).ValidateBlock(profile, block)
	if err != nil {
		return ValidatedPayload{}, err
	}
	var shared testSharedData
	if err := json.Unmarshal(validated.SharedData, &shared); err != nil {
		return ValidatedPayload{}, err
	}
	var localized testLocalizedData
	if err := json.Unmarshal(validated.LocalizedData, &localized); err != nil {
		return ValidatedPayload{}, err
	}
	if shared.Layout == "narrow" && localized.Text == "target-wide-only" {
		return ValidatedPayload{}, errors.New("localized payload is incompatible with narrow layout")
	}
	return validated, nil
}

type testSharedData struct {
	Layout        string `json:"layout,omitempty"`
	FileID        string `json:"fileId,omitempty"`
	MissingFileID string `json:"missingFileId,omitempty"`
	GalleryFileID string `json:"galleryFileId,omitempty"`
	MIMEPrefix    string `json:"mimePrefix,omitempty"`
	Executable    string `json:"executable,omitempty"`
}

type testLocalizedData struct {
	Text string `json:"text,omitempty"`
}

func (testContract) Limits(profile string) (Limits, error) {
	if profile != "post" {
		return Limits{}, fmt.Errorf("unknown profile %q", profile)
	}
	return Limits{MaxBlocks: 5, MaxDepth: 3}, nil
}

func (testContract) ValidateBlock(
	profile string,
	block FullBlock,
) (ValidatedPayload, error) {
	if profile != "post" {
		return ValidatedPayload{}, fmt.Errorf("unknown profile")
	}
	if block.Kind != "paragraph" && block.Kind != "file" && block.Kind != "layout" && block.Kind != "code" {
		return ValidatedPayload{}, fmt.Errorf("unknown kind")
	}
	var shared testSharedData
	if err := strictJSON(block.SharedData, &shared); err != nil {
		return ValidatedPayload{}, err
	}
	var localized testLocalizedData
	if err := strictJSON(block.LocalizedData, &localized); err != nil {
		return ValidatedPayload{}, err
	}
	if block.Kind == "file" && (shared.FileID == "") == (shared.MissingFileID == "") {
		return ValidatedPayload{}, fmt.Errorf("file requires exactly one attachment state")
	}
	if block.Kind != "file" && (shared.FileID != "" || shared.MissingFileID != "") {
		return ValidatedPayload{}, fmt.Errorf("only file Blocks may contain an attachment")
	}
	if block.Kind != "layout" && shared.GalleryFileID != "" {
		return ValidatedPayload{}, fmt.Errorf("only layout Blocks may contain a gallery attachment")
	}
	canonicalShared, _ := json.Marshal(shared)
	canonicalLocalized, _ := json.Marshal(localized)
	validated := ValidatedPayload{SharedData: canonicalShared, LocalizedData: canonicalLocalized}
	fileID := shared.FileID
	missing := false
	if fileID == "" {
		fileID = shared.MissingFileID
		missing = fileID != ""
	}
	if fileID != "" {
		parsed, err := uuid.Parse(fileID)
		if err != nil {
			return ValidatedPayload{}, fmt.Errorf("invalid File UUID")
		}
		missingKind := ""
		if missing {
			missingKind = "file"
		}
		validated.FileReferences = []FileReference{{
			ReferencePath:       "file",
			FileID:              parsed,
			Missing:             missing,
			MissingMediaKind:    missingKind,
			AllowedMIMEPrefixes: []string{shared.MIMEPrefix},
		}}
	}
	if shared.GalleryFileID != "" {
		parsed, err := uuid.Parse(shared.GalleryFileID)
		if err != nil {
			return ValidatedPayload{}, fmt.Errorf("invalid gallery File UUID")
		}
		validated.FileReferences = append(validated.FileReferences, FileReference{
			ReferencePath:       "gallery.items[0].fileId",
			FileID:              parsed,
			AllowedMIMEPrefixes: []string{"image/"},
		})
	}
	return validated, nil
}

func (testContract) ValidateParent(_ string, _ *FullBlock, _ FullBlock) error { return nil }

func (testContract) ValidateLocale(_ string, _ string, localizedData json.RawMessage) (json.RawMessage, error) {
	return canonicalObject(localizedData)
}

func (testContract) BuildExplicitEmptyLocale(_ string, _ string, localizedData json.RawMessage) (json.RawMessage, error) {
	var value map[string]any
	if err := json.Unmarshal(localizedData, &value); err != nil {
		return nil, err
	}
	for key, item := range value {
		if _, ok := item.(string); ok {
			value[key] = ""
		}
	}
	return json.Marshal(value)
}

func (testContract) TranslationSourceChanged(_ string, before, after []FullBlock) (bool, error) {
	return !bytes.Equal(testTranslationSource(before), testTranslationSource(after)), nil
}

func testTranslationSource(blocks []FullBlock) []byte {
	type sourceBlock struct {
		ID            uuid.UUID       `json:"id"`
		ParentID      *uuid.UUID      `json:"parentId,omitempty"`
		ContainerSlot string          `json:"containerSlot"`
		Position      int             `json:"position"`
		Kind          string          `json:"kind"`
		LocalizedData json.RawMessage `json:"localizedData"`
	}
	result := make([]sourceBlock, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, sourceBlock{
			ID: block.ID, ParentID: block.ParentID, ContainerSlot: block.ContainerSlot,
			Position: block.Position, Kind: block.Kind, LocalizedData: block.LocalizedData,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID.String() < result[j].ID.String() })
	encoded, _ := json.Marshal(result)
	return encoded
}

func strictJSON(data json.RawMessage, target any) error {
	if len(bytes.TrimSpace(data)) == 0 {
		data = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

type testReuseAuthorizer struct {
	err   error
	calls []uuid.UUID
}

func (a *testReuseAuthorizer) AuthorizeFileReuse(
	_ context.Context,
	_ *gorm.DB,
	_ Document,
	_ FullBlock,
	_ FileReference,
	file File,
) error {
	a.calls = append(a.calls, file.ID)
	return a.err
}

func newTestStore(t *testing.T) (*gorm.DB, *Store, *testReuseAuthorizer) {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error)
	require.NoError(t, db.AutoMigrate(
		&documentRow{}, &blockRow{}, &blockLocaleRow{}, &fileRow{}, &blockAttachmentRow{},
		&blockAttachmentDownloadAudienceSegmentRow{},
	))
	reuse := &testReuseAuthorizer{}
	store, err := NewStore(testContract{}, reuse)
	require.NoError(t, err)
	return db, store, reuse
}

func TestReplaceBlockAttachmentsPreservesExactSelectorAndResetsReplacementPolicy(t *testing.T) {
	db, _, _ := newTestStore(t)
	blockID := uuid.New()
	firstFileID := uuid.New()
	secondFileID := uuid.New()
	segmentID := uuid.New()
	referencePath := "file"
	restricted := blockAttachmentRow{
		BlockID: blockID, ReferencePath: referencePath, SelectorKind: "active",
		FileID: &firstFileID, DownloadAudience: "restricted",
	}
	require.NoError(t, db.Create(&restricted).Error)
	require.NoError(t, db.Create(&blockAttachmentDownloadAudienceSegmentRow{
		BlockID: blockID, ReferencePath: referencePath, AudienceSegmentID: segmentID,
	}).Error)
	after := newAggregate(Document{})
	after.blocks[blockID] = FullBlock{BaseBlock: BaseBlock{ID: blockID}, FileReferences: []FileReference{{
		BlockID: blockID, ReferencePath: referencePath, FileID: firstFileID,
	}}}
	require.NoError(t, replaceBlockAttachments(t.Context(), db, after, []uuid.UUID{blockID}))
	var preserved blockAttachmentRow
	require.NoError(t, db.Where("block_id = ? AND reference_path = ?", blockID, referencePath).Take(&preserved).Error)
	require.Equal(t, "restricted", preserved.DownloadAudience)
	var segmentCount int64
	require.NoError(t, db.Model(&blockAttachmentDownloadAudienceSegmentRow{}).Where("block_id = ?", blockID).Count(&segmentCount).Error)
	require.Equal(t, int64(1), segmentCount)

	changed := after.blocks[blockID]
	changed.FileReferences[0].FileID = secondFileID
	after.blocks[blockID] = changed
	require.NoError(t, replaceBlockAttachments(t.Context(), db, after, []uuid.UUID{blockID}))
	var reset blockAttachmentRow
	require.NoError(t, db.Where("block_id = ? AND reference_path = ?", blockID, referencePath).Take(&reset).Error)
	require.Equal(t, "disabled", reset.DownloadAudience)
	require.Equal(t, secondFileID, *reset.FileID)
	require.NoError(t, db.Model(&blockAttachmentDownloadAudienceSegmentRow{}).Where("block_id = ?", blockID).Count(&segmentCount).Error)
	require.Zero(t, segmentCount)
}

func createTestDocument(t *testing.T, db *gorm.DB, store *Store) Snapshot {
	t.Helper()
	var snapshot Snapshot
	err := db.Transaction(func(tx *gorm.DB) error {
		created, err := store.CreateDocument(context.Background(), tx, CreateInput{
			Profile: "post", SourceLocale: "en",
		})
		snapshot = created
		return err
	})
	require.NoError(t, err)
	return snapshot
}

func testFence(sourceLocale string) DomainFence {
	return func(context.Context, *gorm.DB, uuid.UUID) (DomainContext, error) {
		return DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func paragraph(id uuid.UUID, position int, layout string) BaseBlock {
	return BaseBlock{
		ID: id, ContainerSlot: "root", Position: position, Kind: "paragraph",
		SharedData: json.RawMessage(fmt.Sprintf(`{"layout":%q}`, layout)),
	}
}

func localeGroup(locale string, values map[uuid.UUID]string) LocaleMutationGroup {
	group := LocaleMutationGroup{Locale: locale}
	ids := make([]uuid.UUID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	for _, id := range ids {
		group.Upserts = append(group.Upserts, LocaleBlockUpdate{
			BlockID: id, LocalizedData: json.RawMessage(fmt.Sprintf(`{"text":%q}`, values[id])),
		})
	}
	return group
}

func applyBatch(t *testing.T, db *gorm.DB, store *Store, batch Batch) (Result, error) {
	t.Helper()
	var result Result
	err := db.Transaction(func(tx *gorm.DB) error {
		applied, err := store.ApplyBatch(context.Background(), tx, batch, testFence("en"))
		result = applied
		return err
	})
	return result, err
}

func TestStoreAppliesAggregateLocalesUnderOneRevision(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	result, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{paragraph(blockID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{blockID: "hello"}),
			localeGroup("ko", map[uuid.UUID]string{blockID: "annyeong"}),
			localeGroup("ja", map[uuid.UUID]string{blockID: "konnichiwa"}),
		},
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, result.ContentChanged)
	require.False(t, result.MetadataChanged)
	require.True(t, result.TranslationSourceChanged)
	loaded, err := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, err)
	require.Len(t, loaded.LocaleOverlays, 3)
	require.Equal(t, []string{"en", "ja", "ko"}, result.ChangedLocales)
	require.NotEmpty(t, loaded.SnapshotDigest)
	var documentCount int64
	require.NoError(t, db.Model(&documentRow{}).Where("id = ?", created.Document.ID).Count(&documentCount).Error)
	require.EqualValues(t, 1, documentCount)
}

func TestStoreAppliesGeneratedContentAndOwningMetadataUnderOneRevision(t *testing.T) {
	db, store, _ := newTestStore(t)
	require.NoError(t, db.Exec(`CREATE TABLE post_ai_metadata (document_id TEXT PRIMARY KEY, title TEXT NOT NULL)`).Error)
	created := createTestDocument(t, db, store)
	require.NoError(t, db.Exec(`INSERT INTO post_ai_metadata (document_id, title) VALUES (?, ?)`, created.Document.ID.String(), "before").Error)
	blockID := uuid.New()

	var result Result
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = store.ApplyBatchWithMetadata(context.Background(), tx, Batch{
			DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
			Upserts:      []BaseBlock{paragraph(blockID, 0, "wide")},
			LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "hello"})},
		}, testFence("en"), func(ctx context.Context, tx *gorm.DB) (MetadataEffect, error) {
			updated := tx.WithContext(ctx).Exec(
				`UPDATE post_ai_metadata SET title = ? WHERE document_id = ? AND title = ?`,
				"after", created.Document.ID.String(), "before",
			)
			return MetadataEffect{
				Changed: updated.RowsAffected == 1, AffectsTranslationSource: updated.RowsAffected == 1,
				SourceLocale: "en", ChangedLocales: []string{"en"},
			}, updated.Error
		})
		return err
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, result.ContentChanged)
	require.True(t, result.MetadataChanged)
	require.True(t, result.TranslationSourceChanged)
	require.Equal(t, []string{"en"}, result.ChangedLocales)
	require.NotEqual(t, created.Document.Revision, result.DocumentRevision)

	loaded, err := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, err)
	require.Equal(t, result.DocumentRevision, loaded.Document.Revision)
	require.Len(t, loaded.Blocks, 1)
	var title string
	require.NoError(t, db.Raw(`SELECT title FROM post_ai_metadata WHERE document_id = ?`, created.Document.ID.String()).Scan(&title).Error)
	require.Equal(t, "after", title)
}

func TestStoreSwitchSourceLocalePreservesExistingValuesAndFillsOnlyMissingBlocks(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	firstID, secondID := uuid.New(), uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{
			paragraph(firstID, 0, "wide"),
			paragraph(secondID, 1, "wide"),
		},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{firstID: "first", secondID: "second"}),
			localeGroup("ko", map[uuid.UUID]string{firstID: "기존"}),
		},
	})
	require.NoError(t, err)

	var switched Result
	err = db.Transaction(func(tx *gorm.DB) error {
		var switchErr error
		switched, switchErr = store.SwitchSourceLocale(
			context.Background(),
			tx,
			SourceLocaleSwitchInput{
				DocumentID: created.Document.ID, ExpectedRevision: seeded.DocumentRevision,
				RequestedLocale: "ko",
			},
			testFence("en"),
			func(context.Context, *gorm.DB) (MetadataEffect, error) {
				return MetadataEffect{
					Changed: true, AffectsTranslationSource: true, SourceLocale: "ko",
				}, nil
			},
		)
		return switchErr
	})
	require.NoError(t, err)
	require.True(t, switched.Changed)
	require.True(t, switched.TranslationSourceChanged)
	require.NotEqual(t, seeded.DocumentRevision, switched.DocumentRevision)

	loaded, err := store.LoadSnapshot(context.Background(), db, created.Document.ID, "ko")
	require.NoError(t, err)
	ko := localeOverlayByBlock(loaded.LocaleOverlays, "ko")
	require.JSONEq(t, `{"text":"기존"}`, string(ko[firstID].LocalizedData))
	require.JSONEq(t, `{}`, string(ko[secondID].LocalizedData))
	en := localeOverlayByBlock(loaded.LocaleOverlays, "en")
	require.JSONEq(t, `{"text":"first"}`, string(en[firstID].LocalizedData))
	require.JSONEq(t, `{"text":"second"}`, string(en[secondID].LocalizedData))
}

func TestStoreSwitchSourceLocaleRejectsUnacceptedPointer(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts:      []BaseBlock{paragraph(blockID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "source"})},
	})
	require.NoError(t, err)

	err = db.Transaction(func(tx *gorm.DB) error {
		_, switchErr := store.SwitchSourceLocale(
			context.Background(),
			tx,
			SourceLocaleSwitchInput{
				DocumentID: created.Document.ID, ExpectedRevision: seeded.DocumentRevision,
				RequestedLocale: "ko",
			},
			testFence("en"),
			func(context.Context, *gorm.DB) (MetadataEffect, error) {
				return MetadataEffect{Changed: true}, nil
			},
		)
		return switchErr
	})
	require.ErrorIs(t, err, ErrInvalidMutation)
	loaded, loadErr := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, loadErr)
	require.Empty(t, localeOverlayByBlock(loaded.LocaleOverlays, "ko"))
}

func TestStoreReportsMetadataOnlyMutationWithoutInventingContentChange(t *testing.T) {
	db, store, _ := newTestStore(t)
	require.NoError(t, db.Exec(`CREATE TABLE post_ai_metadata (document_id TEXT PRIMARY KEY, title TEXT NOT NULL)`).Error)
	created := createTestDocument(t, db, store)
	require.NoError(t, db.Exec(`INSERT INTO post_ai_metadata (document_id, title) VALUES (?, ?)`, created.Document.ID.String(), "before").Error)

	var result Result
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = store.ApplyBatchWithMetadata(context.Background(), tx, Batch{
			DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		}, testFence("en"), func(ctx context.Context, tx *gorm.DB) (MetadataEffect, error) {
			updated := tx.WithContext(ctx).Exec(
				`UPDATE post_ai_metadata SET title = ? WHERE document_id = ? AND title = ?`,
				"after", created.Document.ID.String(), "before",
			)
			return MetadataEffect{Changed: updated.RowsAffected == 1, AffectsTranslationSource: true, ChangedLocales: []string{"en"}}, updated.Error
		})
		return err
	})
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.False(t, result.ContentChanged)
	require.True(t, result.MetadataChanged)
	require.True(t, result.TranslationSourceChanged)
	require.NotEqual(t, created.Document.Revision, result.DocumentRevision)
}

func TestStoreCombinedMutationNoopPreservesRevisionAndFailureRollsBackBothBoundaries(t *testing.T) {
	db, store, _ := newTestStore(t)
	require.NoError(t, db.Exec(`CREATE TABLE post_ai_metadata (document_id TEXT PRIMARY KEY, title TEXT NOT NULL)`).Error)
	created := createTestDocument(t, db, store)
	require.NoError(t, db.Exec(`INSERT INTO post_ai_metadata (document_id, title) VALUES (?, ?)`, created.Document.ID.String(), "before").Error)

	var noop Result
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		noop, err = store.ApplyBatchWithMetadata(context.Background(), tx, Batch{
			DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		}, testFence("en"), func(context.Context, *gorm.DB) (MetadataEffect, error) {
			return MetadataEffect{}, nil
		})
		return err
	})
	require.NoError(t, err)
	require.False(t, noop.Changed)
	require.False(t, noop.ContentChanged)
	require.False(t, noop.MetadataChanged)
	require.Equal(t, created.Document.Revision, noop.DocumentRevision)

	blockID := uuid.New()
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := store.ApplyBatchWithMetadata(context.Background(), tx, Batch{
			DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
			Upserts:      []BaseBlock{paragraph(blockID, 0, "wide")},
			LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "hello"})},
		}, testFence("en"), func(ctx context.Context, tx *gorm.DB) (MetadataEffect, error) {
			if err := tx.WithContext(ctx).Exec(
				`UPDATE post_ai_metadata SET title = ? WHERE document_id = ?`, "after", created.Document.ID.String(),
			).Error; err != nil {
				return MetadataEffect{}, err
			}
			return MetadataEffect{}, errors.New("metadata rejected")
		})
		return err
	})
	require.EqualError(t, err, "metadata rejected")
	loaded, loadErr := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, loadErr)
	require.Empty(t, loaded.Blocks)
	require.Equal(t, created.Document.Revision, loaded.Document.Revision)
	var title string
	require.NoError(t, db.Raw(`SELECT title FROM post_ai_metadata WHERE document_id = ?`, created.Document.ID.String()).Scan(&title).Error)
	require.Equal(t, "before", title)
}

func TestStoreTargetLocaleMutationPreservesSharedRevisionAndDoesNotConflictAcrossLocales(t *testing.T) {
	db, store, _ := newTestStore(t)
	require.NoError(t, db.Exec(`CREATE TABLE target_locale_metadata (
		document_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		writes INTEGER NOT NULL,
		PRIMARY KEY (document_id, locale)
	)`).Error)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	source, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts:      []BaseBlock{paragraph(blockID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "source"})},
	})
	require.NoError(t, err)

	applyTarget := func(locale, value string) Result {
		t.Helper()
		var result Result
		err := db.Transaction(func(tx *gorm.DB) error {
			var applyErr error
			result, applyErr = store.ApplyTargetLocaleBatchWithMetadata(
				context.Background(), tx,
				Batch{
					DocumentID: created.Document.ID, ExpectedRevision: source.DocumentRevision,
					LocaleGroups: []LocaleMutationGroup{localeGroup(locale, map[uuid.UUID]string{blockID: value})},
				},
				locale,
				testFence("en"),
				func(ctx context.Context, tx *gorm.DB, contentChanged bool) (MetadataEffect, error) {
					if !contentChanged {
						return MetadataEffect{}, nil
					}
					write := tx.WithContext(ctx).Exec(
						`INSERT INTO target_locale_metadata (document_id, locale, writes)
						 VALUES (?, ?, 1)
						 ON CONFLICT (document_id, locale) DO UPDATE SET writes = target_locale_metadata.writes + 1`,
						created.Document.ID.String(), locale,
					)
					return MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, write.Error
				},
			)
			return applyErr
		})
		require.NoError(t, err)
		return result
	}

	ko := applyTarget("ko", "target-ko")
	require.True(t, ko.Changed)
	require.True(t, ko.ContentChanged)
	require.Equal(t, source.DocumentRevision, ko.DocumentRevision)
	ja := applyTarget("ja", "target-ja")
	require.True(t, ja.Changed)
	require.Equal(t, source.DocumentRevision, ja.DocumentRevision)

	loaded, err := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, err)
	require.Equal(t, source.DocumentRevision, loaded.Document.Revision)
	require.Len(t, loaded.LocaleOverlays, 3)
}

func TestStoreRejectsNonCanonicalContributorAttribution(t *testing.T) {
	for _, test := range []struct {
		name         string
		contributors func(uuid.UUID, uuid.UUID) []uuid.UUID
	}{
		{
			name: "unsorted",
			contributors: func(first, second uuid.UUID) []uuid.UUID {
				if first.String() < second.String() {
					return []uuid.UUID{second, first}
				}
				return []uuid.UUID{first, second}
			},
		},
		{
			name: "duplicate",
			contributors: func(first, _ uuid.UUID) []uuid.UUID {
				return []uuid.UUID{first, first}
			},
		},
		{
			name: "nil",
			contributors: func(_, _ uuid.UUID) []uuid.UUID {
				return []uuid.UUID{uuid.Nil}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, store, _ := newTestStore(t)
			created := createTestDocument(t, db, store)
			blockID := uuid.New()
			_, err := applyBatch(t, db, store, Batch{
				DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
				Upserts:              []BaseBlock{paragraph(blockID, 0, "wide")},
				LocaleGroups:         []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "hello"})},
				ContributorMemberIDs: test.contributors(uuid.New(), uuid.New()),
			})
			require.ErrorIs(t, err, ErrInvalidMutation)
			loaded, loadErr := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
			require.NoError(t, loadErr)
			require.Empty(t, loaded.Blocks)
		})
	}
}

func TestStoreDerivesSourceImpactAndRejectsRuntimeData(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	inserted, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{paragraph(blockID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{blockID: "hello"}),
			localeGroup("ko", map[uuid.UUID]string{blockID: "annyeong"}),
		},
	})
	require.NoError(t, err)

	layoutOnly, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: inserted.DocumentRevision,
		Upserts: []BaseBlock{paragraph(blockID, 0, "narrow")},
	})
	require.NoError(t, err)
	require.True(t, layoutOnly.Changed)
	require.False(t, layoutOnly.TranslationSourceChanged)
	require.Empty(t, layoutOnly.ChangedLocales)

	sourceEdit, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: layoutOnly.DocumentRevision,
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "changed"})},
	})
	require.NoError(t, err)
	require.True(t, sourceEdit.TranslationSourceChanged)
	require.Equal(t, []string{"en"}, sourceEdit.ChangedLocales)

	targetEdit, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: sourceEdit.DocumentRevision,
		LocaleGroups: []LocaleMutationGroup{localeGroup("ko", map[uuid.UUID]string{blockID: "changed target"})},
	})
	require.NoError(t, err)
	require.False(t, targetEdit.TranslationSourceChanged)
	require.Equal(t, []string{"ko"}, targetEdit.ChangedLocales)

	localeNoOp, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: targetEdit.DocumentRevision,
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "changed"})},
	})
	require.NoError(t, err)
	require.False(t, localeNoOp.Changed)
	require.Empty(t, localeNoOp.ChangedLocales)
	require.Equal(t, targetEdit.DocumentRevision, localeNoOp.DocumentRevision)

	runtime := paragraph(uuid.New(), 1, "wide")
	runtime.SharedData = json.RawMessage(`{"runtime":true}`)
	_, err = applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: localeNoOp.DocumentRevision,
		Upserts:      []BaseBlock{runtime},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{runtime.ID: "bad"})},
	})
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestStoreRejectsStaleCrossDocumentAndCycle(t *testing.T) {
	db, store, _ := newTestStore(t)
	first := createTestDocument(t, db, store)
	second := createTestDocument(t, db, store)
	firstID, secondID := uuid.New(), uuid.New()
	inserted, err := applyBatch(t, db, store, Batch{
		DocumentID: first.Document.ID, ExpectedRevision: first.Document.Revision,
		Upserts:      []BaseBlock{paragraph(firstID, 0, "wide"), paragraph(secondID, 1, "wide")},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{firstID: "one", secondID: "two"})},
	})
	require.NoError(t, err)

	_, err = applyBatch(t, db, store, Batch{
		DocumentID: first.Document.ID, ExpectedRevision: first.Document.Revision,
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{firstID: "stale"})},
	})
	require.ErrorIs(t, err, ErrStaleRevision)

	_, err = applyBatch(t, db, store, Batch{
		DocumentID: second.Document.ID, ExpectedRevision: second.Document.Revision,
		Upserts:      []BaseBlock{paragraph(firstID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{firstID: "steal"})},
	})
	require.ErrorIs(t, err, ErrCrossDocument)

	parentID := secondID
	_, err = applyBatch(t, db, store, Batch{
		DocumentID: first.Document.ID, ExpectedRevision: inserted.DocumentRevision,
		Reorders: []Reorder{
			{BlockID: firstID, ParentID: &parentID, ContainerSlot: "children", Position: 0},
			{BlockID: secondID, ParentID: &firstID, ContainerSlot: "children", Position: 0},
		},
	})
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestStoreRejectsNonDenseSiblingPositions(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	firstID, secondID := uuid.New(), uuid.New()
	_, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{
			paragraph(firstID, 0, "wide"),
			paragraph(secondID, 2, "wide"),
		},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{
			firstID: "first", secondID: "second",
		})},
	})
	require.ErrorIs(t, err, ErrInvalidMutation)

	loaded, loadErr := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, loadErr)
	require.Empty(t, loaded.Blocks)
}

func TestStoreRevalidatesRetainedLocalesForAffectedBaseBlock(t *testing.T) {
	db, _, reuse := newTestStore(t)
	store, err := NewStore(layoutSensitiveLocaleContract{}, reuse)
	require.NoError(t, err)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{paragraph(blockID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{blockID: "source"}),
			localeGroup("ko", map[uuid.UUID]string{blockID: "target-wide-only"}),
		},
	})
	require.NoError(t, err)

	_, err = applyBatch(t, db, store, Batch{
		DocumentID:       created.Document.ID,
		ExpectedRevision: seeded.DocumentRevision,
		Upserts:          []BaseBlock{paragraph(blockID, 0, "narrow")},
	})
	require.ErrorIs(t, err, ErrInvalidMutation)

	loaded, loadErr := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, loadErr)
	require.JSONEq(t, `{"layout":"wide"}`, string(loaded.Blocks[0].SharedData))
}

func TestStoreTargetedEditFailsClosedOnUnrelatedMalformedPersistedJSON(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	firstID, secondID := uuid.New(), uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{paragraph(firstID, 0, "wide"), paragraph(secondID, 1, "wide")},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{
			firstID: "first", secondID: "second",
		})},
	})
	require.NoError(t, err)
	require.NoError(t, db.Model(&blockLocaleRow{}).
		Where("block_id = ? AND locale = ?", secondID, "en").
		Update("localized_data", []byte(`[]`)).Error)

	_, err = applyBatch(t, db, store, Batch{
		DocumentID:       created.Document.ID,
		ExpectedRevision: seeded.DocumentRevision,
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{
			firstID: "edited",
		})},
	})
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestStoreRollsBackWithOwningTransaction(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	rollback := errors.New("rollback outer transaction")
	err := db.Transaction(func(tx *gorm.DB) error {
		_, err := store.ApplyBatch(context.Background(), tx, Batch{
			DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
			Upserts:      []BaseBlock{paragraph(blockID, 0, "wide")},
			LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "temporary"})},
		}, testFence("en"))
		if err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	loaded, err := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, err)
	require.Empty(t, loaded.Blocks)
	require.Equal(t, created.Document.Revision, loaded.Document.Revision)
}

func TestStoreFilePolicyAndMissingRestore(t *testing.T) {
	db, store, reuse := newTestStore(t)
	created := createTestDocument(t, db, store)
	fileID := uuid.New()
	require.NoError(t, db.Create(&fileRow{ID: fileID, MIMEType: "image/png"}).Error)
	blockID := uuid.New()
	base := BaseBlock{
		ID: blockID, ContainerSlot: "root", Kind: "file",
		SharedData: json.RawMessage(fmt.Sprintf(`{"fileId":%q,"mimePrefix":"image/"}`, fileID)),
	}
	attached, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{base},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{blockID: "caption"}),
			localeGroup("ko", map[uuid.UUID]string{blockID: "target"}),
		},
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{fileID}, reuse.calls)
	publicationAttachments, err := store.LoadPublicationAttachments(
		context.Background(), db, created.Document.ID,
	)
	require.NoError(t, err)
	require.Equal(t, []PublicationAttachment{{
		BlockID: blockID, ReferencePath: "file", FileID: fileID,
	}}, publicationAttachments)
	require.NoError(t, db.Model(&blockAttachmentRow{}).
		Where("block_id = ? AND reference_path = ?", blockID, "file").
		Update("selector_kind", "invalid").Error)
	_, err = store.LoadPublicationAttachments(context.Background(), db, created.Document.ID)
	require.ErrorIs(t, err, ErrInvalidMutation)
	require.NoError(t, db.Model(&blockAttachmentRow{}).
		Where("block_id = ? AND reference_path = ?", blockID, "file").
		Update("selector_kind", "active").Error)

	missingID := uuid.New()
	missing := base
	missing.SharedData = json.RawMessage(fmt.Sprintf(`{"missingFileId":%q,"mimePrefix":"image/"}`, missingID))
	_, err = applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: attached.DocumentRevision,
		Upserts: []BaseBlock{missing},
	})
	require.ErrorIs(t, err, ErrFileReference)

	activeAsMissing := base
	activeAsMissing.SharedData = json.RawMessage(fmt.Sprintf(`{"missingFileId":%q,"mimePrefix":"image/"}`, fileID))
	rollbackMissingSelector := errors.New("rollback missing selector probe")
	err = db.Transaction(func(tx *gorm.DB) error {
		_, replaceErr := store.ReplaceSnapshot(context.Background(), tx, ReplaceInput{
			DocumentID: created.Document.ID, ExpectedRevision: attached.DocumentRevision,
			Blocks: []BaseBlock{activeAsMissing},
			LocaleOverlays: []LocaleOverlay{{
				Locale: "en",
				Blocks: []LocaleBlockUpdate{{BlockID: blockID, LocalizedData: json.RawMessage(`{"text":"restored"}`)}},
			}},
		}, testFence("en"))
		if replaceErr != nil {
			return replaceErr
		}
		return rollbackMissingSelector
	})
	require.ErrorIs(t, err, rollbackMissingSelector)
	require.Equal(t, []uuid.UUID{fileID}, reuse.calls, "missing selectors never lock or authorize a File")

	var restored Result
	err = db.Transaction(func(tx *gorm.DB) error {
		result, err := store.ReplaceSnapshot(context.Background(), tx, ReplaceInput{
			DocumentID: created.Document.ID, ExpectedRevision: attached.DocumentRevision,
			Blocks: []BaseBlock{missing},
			LocaleOverlays: []LocaleOverlay{{
				Locale: "en",
				Blocks: []LocaleBlockUpdate{{BlockID: blockID, LocalizedData: json.RawMessage(`{"text":"restored"}`)}},
			}},
		}, testFence("en"))
		restored = result
		return err
	})
	require.NoError(t, err)
	require.True(t, restored.TranslationSourceChanged)
	var attachment blockAttachmentRow
	require.NoError(t, db.Where("block_id = ?", blockID).Take(&attachment).Error)
	require.Equal(t, "missing", attachment.SelectorKind)
	require.Nil(t, attachment.FileID)
	require.NotNil(t, attachment.MissingKind)
	require.Equal(t, "file", *attachment.MissingKind)
	publicationAttachments, err = store.LoadPublicationAttachments(
		context.Background(), db, created.Document.ID,
	)
	require.NoError(t, err)
	require.Equal(t, []PublicationAttachment{{
		BlockID: blockID, ReferencePath: "file", MissingMediaKind: "file",
	}}, publicationAttachments)
	require.NoError(t, db.Model(&blockAttachmentRow{}).
		Where("block_id = ? AND reference_path = ?", blockID, "file").
		Update("missing_kind", "image").Error)
	_, err = store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.ErrorIs(t, err, ErrInvalidMutation)
	require.NoError(t, db.Model(&blockAttachmentRow{}).
		Where("block_id = ? AND reference_path = ?", blockID, "file").
		Update("missing_kind", "file").Error)
	loaded, err := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
	require.NoError(t, err)
	require.Len(t, loaded.LocaleOverlays, 2, "target overlay is preserved for domain staling")
}

func TestStoreRejectsPendingMIMEAndUnauthorizedFilesAtomically(t *testing.T) {
	tests := []struct {
		name, mime    string
		pending, deny bool
	}{
		{name: "pending", mime: "image/png", pending: true},
		{name: "MIME", mime: "audio/wav"},
		{name: "reuse", mime: "image/png", deny: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, store, reuse := newTestStore(t)
			created := createTestDocument(t, db, store)
			fileID, blockID := uuid.New(), uuid.New()
			row := fileRow{ID: fileID, MIMEType: test.mime}
			if test.pending {
				now := store.now()
				row.DeleteRequestedAt = &now
			}
			require.NoError(t, db.Create(&row).Error)
			if test.deny {
				reuse.err = errors.New("not owned")
			}
			_, err := applyBatch(t, db, store, Batch{
				DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
				Upserts: []BaseBlock{{
					ID: blockID, ContainerSlot: "root", Kind: "file",
					SharedData: json.RawMessage(fmt.Sprintf(`{"fileId":%q,"mimePrefix":"image/"}`, fileID)),
				}},
				LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "caption"})},
			})
			require.ErrorIs(t, err, ErrFileReference)
			loaded, loadErr := store.LoadSnapshot(context.Background(), db, created.Document.ID, "en")
			require.NoError(t, loadErr)
			require.Empty(t, loaded.Blocks)
		})
	}
}

func TestStoreIndexesSameFileAcrossBlocksAndNestedReferencePath(t *testing.T) {
	db, store, reuse := newTestStore(t)
	created := createTestDocument(t, db, store)
	fileID := uuid.New()
	require.NoError(t, db.Create(&fileRow{ID: fileID, MIMEType: "image/png"}).Error)
	fileBlockID, galleryBlockID := uuid.New(), uuid.New()

	_, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{
			{
				ID: fileBlockID, ContainerSlot: "root", Position: 0, Kind: "file",
				SharedData: json.RawMessage(fmt.Sprintf(`{"fileId":%q,"mimePrefix":"image/"}`, fileID)),
			},
			{
				ID: galleryBlockID, ContainerSlot: "root", Position: 1, Kind: "layout",
				SharedData: json.RawMessage(fmt.Sprintf(`{"galleryFileId":%q}`, fileID)),
			},
		},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{
			fileBlockID: "file", galleryBlockID: "gallery",
		})},
	})
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{fileID, fileID}, reuse.calls)

	var relations []blockAttachmentRow
	require.NoError(t, db.Order("block_id, reference_path").Find(&relations).Error)
	require.Len(t, relations, 2)
	require.ElementsMatch(t, []string{"file", "gallery.items[0].fileId"}, []string{
		relations[0].ReferencePath, relations[1].ReferencePath,
	})
	for _, relation := range relations {
		require.NotNil(t, relation.FileID)
		require.Equal(t, fileID, *relation.FileID)
		require.Equal(t, "active", relation.SelectorKind)
	}
}

func TestStoreStaleFileAttachPreservesIndependentFile(t *testing.T) {
	db, store, reuse := newTestStore(t)
	created := createTestDocument(t, db, store)
	seedID := uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts:      []BaseBlock{paragraph(seedID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{seedID: "seed"})},
	})
	require.NoError(t, err)
	require.NotEqual(t, created.Document.Revision, seeded.DocumentRevision)

	fileID, fileBlockID := uuid.New(), uuid.New()
	require.NoError(t, db.Create(&fileRow{ID: fileID, MIMEType: "image/png"}).Error)
	_, err = applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{{
			ID: fileBlockID, ContainerSlot: "root", Position: 1, Kind: "file",
			SharedData: json.RawMessage(fmt.Sprintf(`{"fileId":%q,"mimePrefix":"image/"}`, fileID)),
		}},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{fileBlockID: "stale"})},
	})
	require.ErrorIs(t, err, ErrStaleRevision)
	require.Empty(t, reuse.calls, "stale CAS rejects before File policy hooks")

	var fileCount, relationCount int64
	require.NoError(t, db.Model(&fileRow{}).Where("id = ?", fileID).Count(&fileCount).Error)
	require.NoError(t, db.Model(&blockAttachmentRow{}).Where("file_id = ?", fileID).Count(&relationCount).Error)
	require.EqualValues(t, 1, fileCount)
	require.Zero(t, relationCount)
}

func TestStoreAdvanceRevisionUsesServerCallback(t *testing.T) {
	db, store, _ := newTestStore(t)
	created := createTestDocument(t, db, store)
	blockID := uuid.New()
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: created.Document.ID, ExpectedRevision: created.Document.Revision,
		Upserts: []BaseBlock{paragraph(blockID, 0, "wide")},
		LocaleGroups: []LocaleMutationGroup{
			localeGroup("en", map[uuid.UUID]string{blockID: "hello"}),
			localeGroup("ko", map[uuid.UUID]string{blockID: "annyeong"}),
		},
	})
	require.NoError(t, err)
	var changed AdvanceResult
	err = db.Transaction(func(tx *gorm.DB) error {
		result, err := store.AdvanceRevision(context.Background(), tx, AdvanceInput{
			DocumentID: created.Document.ID, ExpectedRevision: seeded.DocumentRevision,
		}, testFence("en"), func(context.Context, *gorm.DB) (MetadataEffect, error) {
			return MetadataEffect{
				Changed: true, AffectsTranslationSource: true, SourceLocale: "ko",
			}, nil
		})
		changed = result
		return err
	})
	require.NoError(t, err)
	require.True(t, changed.Changed)
	require.True(t, changed.TranslationSourceChanged)
	require.NotEqual(t, seeded.DocumentRevision, changed.DocumentRevision)

	called := false
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := store.AdvanceRevision(context.Background(), tx, AdvanceInput{
			DocumentID: created.Document.ID, ExpectedRevision: seeded.DocumentRevision,
		}, testFence("en"), func(context.Context, *gorm.DB) (MetadataEffect, error) {
			called = true
			return MetadataEffect{Changed: true}, nil
		})
		return err
	})
	require.ErrorIs(t, err, ErrStaleRevision)
	require.False(t, called)
}
