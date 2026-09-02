//go:build contentblock_postgres

package contentblock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type hotStatementLogger struct{ count int }

func (counter *hotStatementLogger) LogMode(logger.LogLevel) logger.Interface { return counter }
func (*hotStatementLogger) Info(context.Context, string, ...any)             {}
func (*hotStatementLogger) Warn(context.Context, string, ...any)             {}
func (*hotStatementLogger) Error(context.Context, string, ...any)            {}
func (counter *hotStatementLogger) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count++
}

func TestHotLocalePostgresOneStatement(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := NewGeneratedStore(&testReuseAuthorizer{})
	require.NoError(t, err)
	documentID, blockID := uuid.New(), uuid.New()
	var created Snapshot
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
			ID: documentID, Profile: "post", SourceLocale: "en",
		})
		return createErr
	}))
	replace, err := ReplaceFromRichTextProto(documentID, created.Document.Revision, paragraphDocument(blockID, "before"))
	require.NoError(t, err)
	var seeded Result
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var replaceErr error
		seeded, replaceErr = store.ReplaceSnapshot(t.Context(), tx, replace, testFence("en"))
		return replaceErr
	}))

	batch, err := BatchFromRichTextSystemProto(documentID, hotLocaleProto(blockID, seeded.DocumentRevision, "en", "after"))
	require.NoError(t, err)
	fence := func(context.Context, *gorm.DB, uuid.UUID) (DomainContext, error) {
		return DomainContext{SourceLocale: "en"}, nil
	}
	counter := &hotStatementLogger{}
	measured := db.Session(&gorm.Session{Logger: counter})
	var applied Result
	require.NoError(t, measured.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		applied, applyErr = store.ApplyBatch(t.Context(), tx, batch, fence)
		return applyErr
	}))
	require.Equal(t, 1, counter.count)
	require.True(t, applied.Changed)
	require.True(t, applied.TranslationSourceChanged)

	batch, err = BatchFromRichTextSystemProto(documentID, hotLocaleProto(blockID, applied.DocumentRevision, "en", "after"))
	require.NoError(t, err)
	counter.count = 0
	var noOp Result
	require.NoError(t, measured.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		noOp, applyErr = store.ApplyBatch(t.Context(), tx, batch, fence)
		return applyErr
	}))
	require.Equal(t, 1, counter.count)
	require.False(t, noOp.Changed)
	require.Equal(t, applied.DocumentRevision, noOp.DocumentRevision)

	targetBatch, err := BatchFromRichTextSystemProto(
		documentID,
		hotLocaleProto(blockID, noOp.DocumentRevision, "fr", "target"),
	)
	require.NoError(t, err)
	targetFence := func(context.Context, *gorm.DB, uuid.UUID) (DomainContext, error) {
		return DomainContext{SourceLocale: "en"}, nil
	}
	counter.count = 0
	var target Result
	require.NoError(t, measured.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		target, applyErr = store.ApplyBatch(t.Context(), tx, targetBatch, targetFence)
		return applyErr
	}))
	require.Equal(t, 1, counter.count)
	require.True(t, target.Changed)
	require.False(t, target.TranslationSourceChanged)
}

func TestHotLocalePostgresFlatLatency(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := NewGeneratedStore(&testReuseAuthorizer{})
	require.NoError(t, err)

	for _, count := range []int{1, 10, 100, 1_000} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			documentID := uuid.New()
			blockIDs := make([]uuid.UUID, count)
			for index := range blockIDs {
				blockIDs[index] = uuid.New()
			}
			var created Snapshot
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var createErr error
				created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
					ID: documentID, Profile: "post", SourceLocale: "en",
				})
				return createErr
			}))
			replace, err := ReplaceFromRichTextProto(
				documentID,
				created.Document.Revision,
				hotRichTextDocument(blockIDs, "initial"),
			)
			require.NoError(t, err)
			var seeded Result
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var replaceErr error
				seeded, replaceErr = store.ReplaceSnapshot(t.Context(), tx, replace, testFence("en"))
				return replaceErr
			}))

			revision := seeded.DocumentRevision
			durations := make([]time.Duration, 0, 30)
			for iteration := range 35 {
				text := "A"
				if iteration%2 == 0 {
					text = "B"
				}
				batch, err := BatchFromRichTextSystemProto(
					documentID,
					hotLocaleProto(blockIDs[0], revision, "en", text),
				)
				require.NoError(t, err)
				counter := &hotStatementLogger{}
				var applied Result
				started := time.Now()
				require.NoError(t, db.Session(&gorm.Session{Logger: counter}).Transaction(func(tx *gorm.DB) error {
					var applyErr error
					applied, applyErr = store.ApplyBatch(t.Context(), tx, batch, testFence("en"))
					return applyErr
				}))
				elapsed := time.Since(started)
				require.Equal(t, 1, counter.count)
				require.True(t, applied.Changed)
				revision = applied.DocumentRevision
				if iteration >= 5 {
					durations = append(durations, elapsed)
				}
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p50 := durations[len(durations)/2]
			p95 := durations[(len(durations)*95+99)/100-1]
			t.Logf("blocks=%d statements=1 p50=%s p95=%s", count, p50, p95)
			require.Less(t, p50, 10*time.Millisecond)
		})
	}
}

func TestAffectedReorderPostgresCorrectnessAndScaling(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := NewGeneratedStore(&testReuseAuthorizer{})
	require.NoError(t, err)

	for _, count := range []int{10, 100, 1_000} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			documentID := uuid.New()
			blockIDs := make([]uuid.UUID, count)
			for index := range blockIDs {
				blockIDs[index] = uuid.New()
			}
			var created Snapshot
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var createErr error
				created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
					ID: documentID, Profile: "post", SourceLocale: "en",
				})
				return createErr
			}))
			replace, err := ReplaceFromRichTextProto(
				documentID, created.Document.Revision, hotRichTextDocument(blockIDs, "initial"),
			)
			require.NoError(t, err)
			var seeded Result
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var replaceErr error
				seeded, replaceErr = store.ReplaceSnapshot(t.Context(), tx, replace, testFence("en"))
				return replaceErr
			}))

			reorders := make([]Reorder, count)
			for index, blockID := range blockIDs {
				reorders[index] = Reorder{
					BlockID: blockID, ContainerSlot: "content", Position: count - index - 1,
				}
			}
			counter := &hotStatementLogger{}
			measured := db.Session(&gorm.Session{Logger: counter})
			started := time.Now()
			var applied Result
			require.NoError(t, measured.Transaction(func(tx *gorm.DB) error {
				var applyErr error
				applied, applyErr = store.ApplyBatch(t.Context(), tx, Batch{
					DocumentID: documentID, ExpectedRevision: seeded.DocumentRevision, Reorders: reorders,
				}, testFence("en"))
				return applyErr
			}))
			t.Logf("blocks=%d reorder statements=%d elapsed=%s", count, counter.count, time.Since(started))
			require.True(t, applied.Changed)
			require.True(t, applied.TranslationSourceChanged)

			loaded, err := store.LoadSnapshot(t.Context(), db, documentID, "en")
			require.NoError(t, err)
			require.Len(t, loaded.Blocks, count)
			for index, block := range loaded.Blocks {
				require.Equal(t, blockIDs[count-index-1], block.ID)
			}

			counter.count = 0
			var noOp Result
			require.NoError(t, measured.Transaction(func(tx *gorm.DB) error {
				var applyErr error
				noOp, applyErr = store.ApplyBatch(t.Context(), tx, Batch{
					DocumentID: documentID, ExpectedRevision: applied.DocumentRevision, Reorders: reorders,
				}, testFence("en"))
				return applyErr
			}))
			require.False(t, noOp.Changed)
			require.Equal(t, applied.DocumentRevision, noOp.DocumentRevision)
		})
	}
}

func TestAffectedSharedPresentationPostgresFlatDocumentScope(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := NewGeneratedStore(&testReuseAuthorizer{})
	require.NoError(t, err)

	for _, count := range []int{1, 10, 100, 1_000} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			documentID := uuid.New()
			blockIDs := make([]uuid.UUID, count)
			for index := range blockIDs {
				blockIDs[index] = uuid.New()
			}
			var created Snapshot
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var createErr error
				created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
					ID: documentID, Profile: "post", SourceLocale: "en",
				})
				return createErr
			}))
			replace, err := ReplaceFromRichTextProto(
				documentID, created.Document.Revision, hotRichTextDocument(blockIDs, "initial"),
			)
			require.NoError(t, err)
			var seeded Result
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var replaceErr error
				seeded, replaceErr = store.ReplaceSnapshot(t.Context(), tx, replace, testFence("en"))
				return replaceErr
			}))

			revision := seeded.DocumentRevision
			durations := make([]time.Duration, 0, 30)
			for iteration := range 35 {
				color := "#112233"
				if iteration%2 == 0 {
					color = "#445566"
				}
				mutation := &contentv1.RichTextBlockMutationBatch{
					BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
					Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
					ExpectedRevision:        revision.String(),
					BaseMutations: []*contentv1.RichTextBlockMutation{{
						Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
							Node: &contentv1.RichTextBlockNode{
								Block: &contentv1.RichTextBlock{
									Id: blockIDs[0].String(),
									Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
										Props: &contentv1.ParagraphProps{BackgroundColor: &color},
									}},
								},
								Placement: &contentv1.ContentBlockPlacement{Index: 0},
							},
						}},
					}},
				}
				batch, err := BatchFromRichTextSystemProto(documentID, mutation)
				require.NoError(t, err)
				counter := &hotStatementLogger{}
				started := time.Now()
				var applied Result
				require.NoError(t, db.Session(&gorm.Session{Logger: counter}).Transaction(func(tx *gorm.DB) error {
					var applyErr error
					applied, applyErr = store.ApplyBatch(t.Context(), tx, batch, testFence("en"))
					return applyErr
				}))
				require.True(t, applied.Changed)
				require.Equal(t, 1, counter.count)
				revision = applied.DocumentRevision
				if iteration >= 5 {
					durations = append(durations, time.Since(started))
				}
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p50 := durations[len(durations)/2]
			p95 := durations[(len(durations)*95+99)/100-1]
			t.Logf("blocks=%d shared-one-block statements=1 p50=%s p95=%s", count, p50, p95)
		})
	}
}

func TestAffectedReorderPostgresMovesOneBlockAndRejectsCycle(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := NewStore(testContract{}, &testReuseAuthorizer{})
	require.NoError(t, err)
	documentID := uuid.New()
	var created Snapshot
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
			ID: documentID, Profile: "post", SourceLocale: "en",
		})
		return createErr
	}))
	leftID, rightID, childID := uuid.New(), uuid.New(), uuid.New()
	left := paragraph(leftID, 0, "wide")
	right := paragraph(rightID, 1, "wide")
	child := paragraph(childID, 0, "wide")
	child.ParentID = &leftID
	child.ContainerSlot = "children"
	var seeded Result
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		seeded, applyErr = store.ApplyBatch(t.Context(), tx, Batch{
			DocumentID: documentID, ExpectedRevision: created.Document.Revision,
			Upserts: []BaseBlock{left, right, child},
			LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{
				leftID: "left", rightID: "right", childID: "child",
			})},
		}, testFence("en"))
		return applyErr
	}))

	revision := seeded.DocumentRevision
	durations := make([]time.Duration, 0, 30)
	for iteration := range 35 {
		parentID := rightID
		if iteration%2 != 0 {
			parentID = leftID
		}
		counter := &hotStatementLogger{}
		started := time.Now()
		var moved Result
		require.NoError(t, db.Session(&gorm.Session{Logger: counter}).Transaction(func(tx *gorm.DB) error {
			var applyErr error
			moved, applyErr = store.ApplyBatch(t.Context(), tx, Batch{
				DocumentID: documentID, ExpectedRevision: revision,
				Reorders: []Reorder{{
					BlockID: childID, ParentID: &parentID, ContainerSlot: "children", Position: 0,
				}},
			}, testFence("en"))
			return applyErr
		}))
		require.True(t, moved.Changed)
		require.Equal(t, 1, counter.count)
		revision = moved.DocumentRevision
		if iteration >= 5 {
			durations = append(durations, time.Since(started))
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := durations[len(durations)/2]
	p95 := durations[(len(durations)*95+99)/100-1]
	t.Logf("nested single-move statements=1 p50=%s p95=%s", p50, p95)

	err = db.Transaction(func(tx *gorm.DB) error {
		_, applyErr := store.ApplyBatch(t.Context(), tx, Batch{
			DocumentID: documentID, ExpectedRevision: revision,
			Reorders: []Reorder{{
				BlockID: rightID, ParentID: &childID, ContainerSlot: "children", Position: 0,
			}},
		}, testFence("en"))
		return applyErr
	})
	require.ErrorIs(t, err, ErrInvalidMutation)
}

func TestAffectedReorderPostgresLatency(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	store, err := NewGeneratedStore(&testReuseAuthorizer{})
	require.NoError(t, err)

	for _, count := range []int{10, 100, 1_000} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			documentID := uuid.New()
			blockIDs := make([]uuid.UUID, count)
			for index := range blockIDs {
				blockIDs[index] = uuid.New()
			}
			var created Snapshot
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var createErr error
				created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
					ID: documentID, Profile: "post", SourceLocale: "en",
				})
				return createErr
			}))
			replace, err := ReplaceFromRichTextProto(
				documentID, created.Document.Revision, hotRichTextDocument(blockIDs, "initial"),
			)
			require.NoError(t, err)
			var seeded Result
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				var replaceErr error
				seeded, replaceErr = store.ReplaceSnapshot(t.Context(), tx, replace, testFence("en"))
				return replaceErr
			}))
			revision := seeded.DocumentRevision
			durations := make([]time.Duration, 0, 30)
			for iteration := range 35 {
				reversed := iteration%2 == 0
				reorders := make([]Reorder, count)
				for index, blockID := range blockIDs {
					position := index
					if reversed {
						position = count - index - 1
					}
					reorders[index] = Reorder{BlockID: blockID, ContainerSlot: "content", Position: position}
				}
				counter := &hotStatementLogger{}
				started := time.Now()
				var applied Result
				require.NoError(t, db.Session(&gorm.Session{Logger: counter}).Transaction(func(tx *gorm.DB) error {
					var applyErr error
					applied, applyErr = store.ApplyBatch(t.Context(), tx, Batch{
						DocumentID: documentID, ExpectedRevision: revision, Reorders: reorders,
					}, testFence("en"))
					return applyErr
				}))
				require.True(t, applied.Changed)
				require.Equal(t, 1, counter.count)
				revision = applied.DocumentRevision
				if iteration >= 5 {
					durations = append(durations, time.Since(started))
				}
			}
			sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
			p50 := durations[len(durations)/2]
			p95 := durations[(len(durations)*95+99)/100-1]
			t.Logf("blocks=%d full-reorder statements=1 p50=%s p95=%s", count, p50, p95)
		})
	}
}

func TestHotSharedPostgresFilePolicyAndRollback(t *testing.T) {
	dsn := os.Getenv("CONTENTBLOCK_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONTENTBLOCK_POSTGRES_DSN is required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	reuse := &testReuseAuthorizer{}
	store, err := NewStore(testContract{}, reuse)
	require.NoError(t, err)
	documentID, blockID := uuid.New(), uuid.New()
	var created Snapshot
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(t.Context(), tx, CreateInput{
			ID: documentID, Profile: "post", SourceLocale: "en",
		})
		return createErr
	}))
	activeID, wrongMIMEID, pendingID, replacementID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	insertPostgresFile(t, db, activeID, "image/png", false)
	insertPostgresFile(t, db, wrongMIMEID, "audio/wav", false)
	insertPostgresFile(t, db, pendingID, "image/png", true)
	insertPostgresFile(t, db, replacementID, "image/png", false)
	base := BaseBlock{
		ID: blockID, ContainerSlot: "root", Kind: "file",
		SharedData: json.RawMessage(fmt.Sprintf(`{"fileId":%q,"mimePrefix":"image/"}`, activeID)),
	}
	seeded, err := applyBatch(t, db, store, Batch{
		DocumentID: documentID, ExpectedRevision: created.Document.Revision,
		Upserts:      []BaseBlock{base},
		LocaleGroups: []LocaleMutationGroup{localeGroup("en", map[uuid.UUID]string{blockID: "file"})},
	})
	require.NoError(t, err)
	segmentID := uuid.New()
	require.NoError(t, db.Exec(`
		INSERT INTO audience_segment (id, name, segment_type, created_at)
		VALUES (?, 'Hot shared policy', 'SEGMENT_TYPE_MEMBER_TAGS', NOW())
	`, segmentID).Error)
	require.NoError(t, db.Exec(`
		UPDATE content_block_attachment SET download_audience = 'restricted'
		WHERE block_id = ? AND reference_path = 'file'
	`, blockID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment_download_audience_segment (
			block_id, reference_path, audience_segment_id
		) VALUES (?, 'file', ?)
	`, blockID, segmentID).Error)
	reuse.calls = nil

	applyFile := func(expected, fileID uuid.UUID) (Result, int, error) {
		candidate := base
		candidate.SharedData = json.RawMessage(fmt.Sprintf(`{"fileId":%q,"mimePrefix":"image/"}`, fileID))
		reference := FileReference{
			BlockID: blockID, ReferencePath: "file", FileID: fileID,
			AllowedMIMEPrefixes: []string{"image/"},
		}
		counter := &hotStatementLogger{}
		var result Result
		err := db.Session(&gorm.Session{Logger: counter}).Transaction(func(tx *gorm.DB) error {
			var applyErr error
			result, applyErr = store.ApplyBatch(t.Context(), tx, Batch{
				DocumentID: documentID, ExpectedRevision: expected,
				Upserts: []BaseBlock{candidate}, validatedProfile: "post",
				validatedBaseReferences: map[uuid.UUID][]FileReference{blockID: {reference}},
			}, testFence("en"))
			return applyErr
		})
		return result, counter.count, err
	}

	_, statements, err := applyFile(seeded.DocumentRevision, wrongMIMEID)
	require.ErrorIs(t, err, ErrFileReference)
	require.Equal(t, 1, statements)
	requirePostgresAttachment(t, db, blockID, activeID)

	_, statements, err = applyFile(seeded.DocumentRevision, pendingID)
	require.ErrorIs(t, err, ErrFileReference)
	require.Equal(t, 1, statements)
	requirePostgresAttachment(t, db, blockID, activeID)

	missingCandidate := base
	missingCandidate.SharedData = json.RawMessage(fmt.Sprintf(`{"missingFileId":%q,"mimePrefix":"image/"}`, replacementID))
	counter := &hotStatementLogger{}
	err = db.Session(&gorm.Session{Logger: counter}).Transaction(func(tx *gorm.DB) error {
		_, applyErr := store.ApplyBatch(t.Context(), tx, Batch{
			DocumentID: documentID, ExpectedRevision: seeded.DocumentRevision,
			Upserts: []BaseBlock{missingCandidate}, validatedProfile: "post",
			validatedBaseReferences: map[uuid.UUID][]FileReference{blockID: {{
				BlockID: blockID, ReferencePath: "file", FileID: replacementID,
				Missing: true, MissingMediaKind: "file", AllowedMIMEPrefixes: []string{"image/"},
			}}},
		}, testFence("en"))
		return applyErr
	})
	require.ErrorIs(t, err, ErrFileReference)
	require.Equal(t, 1, counter.count)
	requirePostgresAttachment(t, db, blockID, activeID)

	reuse.err = errors.New("denied")
	_, statements, err = applyFile(seeded.DocumentRevision, replacementID)
	require.ErrorIs(t, err, ErrFileReference)
	require.Equal(t, 1, statements)
	requirePostgresAttachment(t, db, blockID, activeID)

	reuse.err = nil
	applied, statements, err := applyFile(seeded.DocumentRevision, replacementID)
	require.NoError(t, err)
	require.Equal(t, 1, statements)
	require.True(t, applied.Changed)
	require.Equal(t, []uuid.UUID{replacementID, replacementID}, reuse.calls)
	requirePostgresAttachment(t, db, blockID, replacementID)
	var audience string
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Pluck("download_audience", &audience).Error)
	require.Equal(t, "disabled", audience)
	var policySegments int64
	require.NoError(t, db.Table("content_block_attachment_download_audience_segment").
		Where("block_id = ? AND reference_path = 'file'", blockID).Count(&policySegments).Error)
	require.Zero(t, policySegments)

	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Update("download_audience", "public").Error)
	reuse.calls = nil
	noOp, statements, err := applyFile(applied.DocumentRevision, replacementID)
	require.NoError(t, err)
	require.Equal(t, 1, statements)
	require.False(t, noOp.Changed)
	require.Empty(t, reuse.calls)
	require.NoError(t, db.Table("content_block_attachment").
		Where("block_id = ? AND reference_path = 'file'", blockID).
		Pluck("download_audience", &audience).Error)
	require.Equal(t, "public", audience)

	_, statements, err = applyFile(seeded.DocumentRevision, activeID)
	require.ErrorIs(t, err, ErrStaleRevision)
	require.Equal(t, 1, statements)
	requirePostgresAttachment(t, db, blockID, replacementID)

	require.NoError(t, db.Exec(`UPDATE content_block SET shared_data = '{"unknown":true}'::jsonb WHERE id = ?`, blockID).Error)
	_, statements, err = applyFile(applied.DocumentRevision, activeID)
	require.ErrorIs(t, err, ErrInvalidMutation)
	require.Equal(t, 1, statements)
	requirePostgresAttachment(t, db, blockID, replacementID)
	var persisted struct {
		Revision   uuid.UUID       `gorm:"column:revision"`
		SharedData json.RawMessage `gorm:"column:shared_data"`
	}
	require.NoError(t, db.Table("content_document AS document").
		Select("document.revision, block.shared_data").
		Joins("JOIN content_block AS block ON block.document_id = document.id").
		Where("document.id = ? AND block.id = ?", documentID, blockID).Take(&persisted).Error)
	require.Equal(t, applied.DocumentRevision, persisted.Revision)
	require.JSONEq(t, `{"unknown":true}`, string(persisted.SharedData))
}

func insertPostgresFile(t *testing.T, db *gorm.DB, id uuid.UUID, mime string, pending bool) {
	t.Helper()
	var deleteRequested any
	if pending {
		deleteRequested = time.Now().UTC()
	}
	require.NoError(t, db.Exec(`
INSERT INTO file(id, file_name, mime_type, file_size, extension, sha256, delete_requested_at)
VALUES (?, 'fixture', ?, 32, 'bin', decode(repeat('01', 32), 'hex'), ?)
`, id, mime, deleteRequested).Error)
}

func requirePostgresAttachment(t *testing.T, db *gorm.DB, blockID, fileID uuid.UUID) {
	t.Helper()
	var row struct {
		FileID uuid.UUID `gorm:"column:file_id"`
	}
	require.NoError(t, db.Table("content_block_attachment").
		Select("file_id").Where("block_id = ? AND reference_path = 'file'", blockID).
		Take(&row).Error)
	require.Equal(t, fileID, row.FileID)
}

func hotLocaleProto(blockID, revision uuid.UUID, locale, text string) *contentv1.RichTextBlockMutationBatch {
	return &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: locale,
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: &contentv1.RichTextBlockLocale{
						BlockId: blockID.String(),
						Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
							Props: &contentv1.ParagraphLocaleProps{},
							Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: text},
							}}},
						}},
					},
				}},
			}},
		}},
	}
}

func hotRichTextDocument(blockIDs []uuid.UUID, text string) *contentv1.RichTextDocument {
	nodes := make([]*contentv1.RichTextBlockNode, len(blockIDs))
	localized := make([]*contentv1.RichTextBlockLocale, len(blockIDs))
	for index, blockID := range blockIDs {
		nodes[index] = &contentv1.RichTextBlockNode{
			Block: &contentv1.RichTextBlock{
				Id: blockID.String(),
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
					Props: &contentv1.ParagraphProps{},
				}},
			},
			Placement: &contentv1.ContentBlockPlacement{Index: uint32(index)},
		}
		localized[index] = &contentv1.RichTextBlockLocale{
			BlockId: blockID.String(),
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
					Text: &contentv1.RichTextStyledText{Text: text},
				}}},
			}},
		}
	}
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            "en",
		Base:                    &contentv1.RichTextBlockGraph{Nodes: nodes},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: "en", Blocks: localized,
		}},
	}
}
