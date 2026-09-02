package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var blockCounts = []int{1, 10, 100, 1_000}

type statementCounter struct {
	mu       sync.Mutex
	count    int
	duration time.Duration
}

func (counter *statementCounter) LogMode(logger.LogLevel) logger.Interface { return counter }
func (*statementCounter) Info(context.Context, string, ...any)             {}
func (*statementCounter) Warn(context.Context, string, ...any)             {}
func (*statementCounter) Error(context.Context, string, ...any)            {}
func (counter *statementCounter) Trace(
	_ context.Context,
	started time.Time,
	_ func() (string, int64),
	_ error,
) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	counter.count++
	counter.duration += time.Since(started)
}

func (counter *statementCounter) reset() {
	counter.mu.Lock()
	counter.count = 0
	counter.duration = 0
	counter.mu.Unlock()
}

func (counter *statementCounter) value() (int, time.Duration) {
	counter.mu.Lock()
	defer counter.mu.Unlock()
	return counter.count, counter.duration
}

type benchmarkFileReuse struct{}

func (benchmarkFileReuse) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

type sample struct {
	TotalMilliseconds      float64 `json:"totalMilliseconds"`
	BeginMilliseconds      float64 `json:"beginMilliseconds"`
	BodySQLMilliseconds    float64 `json:"bodySqlMilliseconds"`
	NonSQLBodyMilliseconds float64 `json:"nonSqlBodyMilliseconds"`
	CommitMilliseconds     float64 `json:"commitMilliseconds"`
	BodyStatements         int     `json:"bodyStatements"`
	RowsRead               int64   `json:"rowsRead"`
	RowsWritten            int64   `json:"rowsWritten"`
}

type durationSummary struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

type operationSummary struct {
	Repetitions             int             `json:"repetitions"`
	BodyStatements          []int           `json:"bodyStatements"`
	TransactionMilliseconds durationSummary `json:"transactionMilliseconds"`
	BeginMilliseconds       durationSummary `json:"beginMilliseconds"`
	BodySQLMilliseconds     durationSummary `json:"bodySqlMilliseconds"`
	NonSQLBodyMilliseconds  durationSummary `json:"nonSqlBodyMilliseconds"`
	CommitMilliseconds      durationSummary `json:"commitMilliseconds"`
	RowsRead                int64           `json:"rowsRead"`
	RowsWritten             int64           `json:"rowsWritten"`
}

type fixture struct {
	count       int
	documentID  uuid.UUID
	revision    uuid.UUID
	blockIDs    []uuid.UUID
	originalYJS []byte
	editedYJS   []byte
	reversedYJS []byte
}

type fileFixture struct {
	documentID uuid.UUID
	revision   uuid.UUID
	blockID    uuid.UUID
	fileIDs    [2]uuid.UUID
}

type nestedFixture struct {
	documentID uuid.UUID
	revision   uuid.UUID
	leftID     uuid.UUID
	rightID    uuid.UUID
	childID    uuid.UUID
}

type caseResult struct {
	Blocks                  int               `json:"blocks"`
	YJSBytes                map[string]int    `json:"yjsBytes"`
	OldWholeYJSSingleEdit   operationSummary  `json:"oldWholeYjsSingleEdit"`
	OldWholeYJSFullReorder  *operationSummary `json:"oldWholeYjsFullReorder,omitempty"`
	TypedSingleLocaleEdit   operationSummary  `json:"typedSingleLocaleEdit"`
	TypedSharedOneBlock     operationSummary  `json:"typedSharedOneBlock"`
	TypedSharedOneBlockFile operationSummary  `json:"typedSharedOneBlockFile"`
	TypedFullReorder        *operationSummary `json:"typedFullReorder,omitempty"`
}

type output struct {
	Environment      map[string]any   `json:"environment"`
	Results          []caseResult     `json:"results"`
	NestedSingleMove operationSummary `json:"nestedSingleMove"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("BENCHMARK_DATABASE_DSN")
	fixtureDirectory := os.Getenv("BENCHMARK_YJS_FIXTURE_DIR")
	if dsn == "" || fixtureDirectory == "" {
		return errors.New("BENCHMARK_DATABASE_DSN and BENCHMARK_YJS_FIXTURE_DIR are required")
	}
	warmups := envPositiveInt("BENCHMARK_WARMUPS", 5)
	repetitions := envPositiveInt("BENCHMARK_REPETITIONS", 30)
	counter := &statementCounter{}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: counter})
	if err != nil {
		return fmt.Errorf("open benchmark database: %w", err)
	}
	sqlDatabase, err := database.DB()
	if err != nil {
		return fmt.Errorf("open SQL database: %w", err)
	}
	defer sqlDatabase.Close()
	sqlDatabase.SetMaxOpenConns(1)
	sqlDatabase.SetMaxIdleConns(1)
	ctx := context.Background()
	store, err := contentblock.NewGeneratedStore(benchmarkFileReuse{})
	if err != nil {
		return fmt.Errorf("construct generated content Block Store: %w", err)
	}

	if err := database.Exec(`
DROP TABLE IF EXISTS benchmark_legacy_document;
CREATE TABLE benchmark_legacy_document (
  operation text NOT NULL,
  size_blocks integer NOT NULL,
  revision uuid NOT NULL,
  yjs_state bytea NOT NULL,
  edit_hash text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (operation, size_blocks)
)`).Error; err != nil {
		return fmt.Errorf("create legacy benchmark table: %w", err)
	}

	var serverVersion, fsync, synchronousCommit string
	if err := sqlDatabase.QueryRowContext(ctx, `
SELECT current_setting('server_version'), current_setting('fsync'), current_setting('synchronous_commit')
`).Scan(&serverVersion, &fsync, &synchronousCommit); err != nil {
		return fmt.Errorf("read PostgreSQL settings: %w", err)
	}

	result := output{
		Environment: map[string]any{
			"go":                  runtime.Version(),
			"platform":            runtime.GOOS + "/" + runtime.GOARCH,
			"postgres":            serverVersion,
			"fsync":               fsync,
			"synchronousCommit":   synchronousCommit,
			"warmups":             warmups,
			"repetitions":         repetitions,
			"statementDefinition": "GORM Trace calls inside the timed Store transaction body; BEGIN, COMMIT, and row-stat probes excluded",
			"rowDefinition":       "first-warmup transaction delta of pg_stat_xact_user_tables tuple reads/fetches and inserts/updates/deletes; probes excluded from timed samples",
			"scope":               "generated typed adapters plus exact Store transaction; no-op benchmark File reuse authorizer included, authorization/domain fence/provider/network excluded",
		},
		Results: make([]caseResult, 0, len(blockCounts)),
	}

	for _, count := range blockCounts {
		current, err := prepareFixture(ctx, database, store, fixtureDirectory, count)
		if err != nil {
			return err
		}
		if err := insertLegacyFixtures(ctx, sqlDatabase, current); err != nil {
			return err
		}

		legacyEdit, err := measure(repetitions, warmups, func(iteration int) (sample, error) {
			payload := current.originalYJS
			if iteration%2 == 0 {
				payload = current.editedYJS
			}
			return measureLegacy(ctx, sqlDatabase, "single_edit", count, payload, iteration)
		})
		if err != nil {
			return fmt.Errorf("measure %d-Block legacy edit: %w", count, err)
		}
		editState := 0
		var editRows sample
		typedEdit, err := measure(repetitions, warmups, func(iteration int) (sample, error) {
			editState = 1 - editState
			text := "A representative paragraph body used for repeatable Block persistence measurement."
			if editState == 1 {
				text += " edited"
			}
			batch, batchErr := localeBatch(current.documentID, current.revision, current.blockIDs[0], text)
			if batchErr != nil {
				return sample{}, batchErr
			}
			var applied contentblock.Result
			captureRows := iteration == 0
			measured, measureErr := measureStoreTransaction(ctx, database, counter, captureRows, func(tx *gorm.DB) error {
				var applyErr error
				applied, applyErr = store.ApplyBatch(ctx, tx, batch, domainFence)
				return applyErr
			})
			if captureRows {
				editRows = measured
			}
			if measureErr == nil {
				current.revision = applied.DocumentRevision
			}
			return measured, measureErr
		})
		if err != nil {
			return fmt.Errorf("measure %d-Block typed edit: %w", count, err)
		}

		sharedState := 0
		var sharedRows sample
		typedShared, err := measure(repetitions, warmups, func(iteration int) (sample, error) {
			sharedState = 1 - sharedState
			color := "#112233"
			if sharedState == 1 {
				color = "#445566"
			}
			batch, batchErr := sharedParagraphBatch(current.documentID, current.revision, current.blockIDs[0], color)
			if batchErr != nil {
				return sample{}, batchErr
			}
			var applied contentblock.Result
			captureRows := iteration == 0
			measured, measureErr := measureStoreTransaction(ctx, database, counter, captureRows, func(tx *gorm.DB) error {
				var applyErr error
				applied, applyErr = store.ApplyBatch(ctx, tx, batch, domainFence)
				return applyErr
			})
			if captureRows {
				sharedRows = measured
			}
			if measureErr == nil {
				current.revision = applied.DocumentRevision
			}
			return measured, measureErr
		})
		if err != nil {
			return fmt.Errorf("measure %d-Block shared edit: %w", count, err)
		}

		withFile, err := prepareFileFixture(ctx, database, store, count)
		if err != nil {
			return err
		}
		fileState := 0
		var fileRows sample
		typedFile, err := measure(repetitions, warmups, func(iteration int) (sample, error) {
			fileState = 1 - fileState
			batch, batchErr := sharedFileBatch(
				withFile.documentID, withFile.revision, withFile.blockID, withFile.fileIDs[fileState],
			)
			if batchErr != nil {
				return sample{}, batchErr
			}
			var applied contentblock.Result
			captureRows := iteration == 0
			measured, measureErr := measureStoreTransaction(ctx, database, counter, captureRows, func(tx *gorm.DB) error {
				var applyErr error
				applied, applyErr = store.ApplyBatch(ctx, tx, batch, domainFence)
				return applyErr
			})
			if captureRows {
				fileRows = measured
			}
			if measureErr == nil {
				withFile.revision = applied.DocumentRevision
			}
			return measured, measureErr
		})
		if err != nil {
			return fmt.Errorf("measure %d-Block shared File edit: %w", count, err)
		}
		if err := cleanupFileFixture(ctx, database, withFile); err != nil {
			return err
		}

		var legacyReorderSummary, typedReorderSummary *operationSummary
		if count >= 10 {
			legacyReorder, measureErr := measure(repetitions, warmups, func(iteration int) (sample, error) {
				payload := current.originalYJS
				if iteration%2 == 0 {
					payload = current.reversedYJS
				}
				return measureLegacy(ctx, sqlDatabase, "full_reorder", count, payload, iteration)
			})
			if measureErr != nil {
				return fmt.Errorf("measure %d-Block legacy reorder: %w", count, measureErr)
			}
			legacySummary := summarize(legacyReorder, repetitions, sample{RowsRead: 1, RowsWritten: 1})
			legacyReorderSummary = &legacySummary

			reversed := false
			var reorderRows sample
			typedReorder, measureErr := measure(repetitions, warmups, func(iteration int) (sample, error) {
				reversed = !reversed
				reorders := make([]contentblock.Reorder, 0, count)
				for position, blockID := range current.blockIDs {
					nextPosition := position
					if reversed {
						nextPosition = count - position - 1
					}
					reorders = append(reorders, contentblock.Reorder{
						BlockID: blockID, ContainerSlot: "content", Position: nextPosition,
					})
				}
				batch := contentblock.Batch{
					DocumentID: current.documentID, ExpectedRevision: current.revision, Reorders: reorders,
				}
				var applied contentblock.Result
				captureRows := iteration == 0
				measured, applyErr := measureStoreTransaction(ctx, database, counter, captureRows, func(tx *gorm.DB) error {
					var storeErr error
					applied, storeErr = store.ApplyBatch(ctx, tx, batch, domainFence)
					return storeErr
				})
				if captureRows {
					reorderRows = measured
				}
				if applyErr == nil {
					current.revision = applied.DocumentRevision
				}
				return measured, applyErr
			})
			if measureErr != nil {
				return fmt.Errorf("measure %d-Block typed reorder: %w", count, measureErr)
			}
			reorderSummary := summarize(typedReorder, repetitions, reorderRows)
			typedReorderSummary = &reorderSummary
		}

		result.Results = append(result.Results, caseResult{
			Blocks: count,
			YJSBytes: map[string]int{
				"original": len(current.originalYJS), "singleEdit": len(current.editedYJS), "fullReorder": len(current.reversedYJS),
			},
			OldWholeYJSSingleEdit:   summarize(legacyEdit, repetitions, sample{RowsRead: 1, RowsWritten: 1}),
			OldWholeYJSFullReorder:  legacyReorderSummary,
			TypedSingleLocaleEdit:   summarize(typedEdit, repetitions, editRows),
			TypedSharedOneBlock:     summarize(typedShared, repetitions, sharedRows),
			TypedSharedOneBlockFile: summarize(typedFile, repetitions, fileRows),
			TypedFullReorder:        typedReorderSummary,
		})
		if err := database.WithContext(ctx).Exec(
			"DELETE FROM content_document WHERE id = ?", current.documentID,
		).Error; err != nil {
			return fmt.Errorf("delete %d-Block benchmark document: %w", count, err)
		}
	}

	nested, err := prepareNestedFixture(ctx, database, store)
	if err != nil {
		return err
	}
	moveRight := false
	var nestedRows sample
	nestedSamples, err := measure(repetitions, warmups, func(iteration int) (sample, error) {
		moveRight = !moveRight
		parentID := nested.leftID
		if moveRight {
			parentID = nested.rightID
		}
		batch := contentblock.Batch{
			DocumentID: nested.documentID, ExpectedRevision: nested.revision,
			Reorders: []contentblock.Reorder{{
				BlockID: nested.childID, ParentID: &parentID, ContainerSlot: "content", Position: 0,
			}},
		}
		var applied contentblock.Result
		captureRows := iteration == 0
		measured, measureErr := measureStoreTransaction(ctx, database, counter, captureRows, func(tx *gorm.DB) error {
			var applyErr error
			applied, applyErr = store.ApplyBatch(ctx, tx, batch, domainFence)
			return applyErr
		})
		if captureRows {
			nestedRows = measured
		}
		if measureErr == nil {
			nested.revision = applied.DocumentRevision
		}
		return measured, measureErr
	})
	if err != nil {
		return fmt.Errorf("measure nested single move: %w", err)
	}
	result.NestedSingleMove = summarize(nestedSamples, repetitions, nestedRows)

	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode benchmark result: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func domainFence(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
	return contentblock.DomainContext{SourceLocale: "en"}, nil
}

func prepareFixture(
	ctx context.Context,
	database *gorm.DB,
	store *contentblock.Store,
	fixtureDirectory string,
	count int,
) (*fixture, error) {
	load := func(name string) ([]byte, error) {
		value, err := os.ReadFile(filepath.Join(fixtureDirectory, fmt.Sprintf("%d-%s.bin", count, name)))
		if err != nil {
			return nil, fmt.Errorf("read %d-Block %s Yjs fixture: %w", count, name, err)
		}
		return value, nil
	}
	original, err := load("original")
	if err != nil {
		return nil, err
	}
	edited, err := load("edited")
	if err != nil {
		return nil, err
	}
	reversed, err := load("reversed")
	if err != nil {
		return nil, err
	}
	documentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-benchmark-document-%d", count)))
	blockIDs := make([]uuid.UUID, count)
	for index := range count {
		blockID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-benchmark-%d-%d", count, index)))
		blockIDs[index] = blockID
	}
	seededRevision, err := seedRichTextDocument(ctx, database, store, documentID, paragraphDocument(blockIDs))
	if err != nil {
		return nil, fmt.Errorf("seed %d-Block fixture document: %w", count, err)
	}
	return &fixture{
		count: count, documentID: documentID, revision: seededRevision, blockIDs: blockIDs,
		originalYJS: original, editedYJS: edited, reversedYJS: reversed,
	}, nil
}

func seedRichTextDocument(
	ctx context.Context,
	database *gorm.DB,
	store *contentblock.Store,
	documentID uuid.UUID,
	document *contentv1.RichTextDocument,
) (uuid.UUID, error) {
	var created contentblock.Snapshot
	if err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(ctx, tx, contentblock.CreateInput{
			ID: documentID, Profile: "post", SourceLocale: "en",
		})
		return createErr
	}); err != nil {
		return uuid.Nil, err
	}
	replace, err := contentblock.ReplaceFromRichTextProto(documentID, created.Document.Revision, document)
	if err != nil {
		return uuid.Nil, err
	}
	var seeded contentblock.Result
	if err := database.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var replaceErr error
		seeded, replaceErr = store.ReplaceSnapshot(ctx, tx, replace, domainFence)
		return replaceErr
	}); err != nil {
		return uuid.Nil, err
	}
	return seeded.DocumentRevision, nil
}

func paragraphDocument(blockIDs []uuid.UUID) *contentv1.RichTextDocument {
	nodes := make([]*contentv1.RichTextBlockNode, len(blockIDs))
	localized := make([]*contentv1.RichTextBlockLocale, len(blockIDs))
	for index, blockID := range blockIDs {
		nodes[index] = paragraphNode(blockID, nil, index, nil)
		localized[index] = paragraphLocale(
			blockID,
			"A representative paragraph body used for repeatable Block persistence measurement.",
		)
	}
	return richTextDocument(nodes, localized)
}

func richTextDocument(
	nodes []*contentv1.RichTextBlockNode,
	localized []*contentv1.RichTextBlockLocale,
) *contentv1.RichTextDocument {
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

func paragraphNode(
	blockID uuid.UUID,
	parentID *uuid.UUID,
	position int,
	backgroundColor *string,
) *contentv1.RichTextBlockNode {
	var parent *string
	if parentID != nil {
		value := parentID.String()
		parent = &value
	}
	return &contentv1.RichTextBlockNode{
		Block: &contentv1.RichTextBlock{
			Id: blockID.String(),
			Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
				Props: &contentv1.ParagraphProps{BackgroundColor: backgroundColor},
			}},
		},
		Placement: &contentv1.ContentBlockPlacement{ParentBlockId: parent, Index: uint32(position)},
	}
}

func paragraphLocale(blockID uuid.UUID, text string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID.String(),
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		}},
	}
}

func localeBatch(documentID, revision, blockID uuid.UUID, text string) (contentblock.Batch, error) {
	return contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		LocaleMutationGroups: []*contentv1.RichTextLocaleMutationGroup{{
			Locale: "en",
			Mutations: []*contentv1.RichTextBlockLocaleMutation{{
				Operation: &contentv1.RichTextBlockLocaleMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlockLocale{
					Block: paragraphLocale(blockID, text),
				}},
			}},
		}},
	})
}

func sharedParagraphBatch(
	documentID, revision, blockID uuid.UUID,
	backgroundColor string,
) (contentblock.Batch, error) {
	return contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: paragraphNode(blockID, nil, 0, &backgroundColor),
			}},
		}},
	})
}

func prepareFileFixture(
	ctx context.Context,
	database *gorm.DB,
	store *contentblock.Store,
	count int,
) (*fileFixture, error) {
	documentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-file-benchmark-document-%d", count)))
	blockID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-file-benchmark-block-%d", count)))
	fileIDs := [2]uuid.UUID{
		uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-file-benchmark-a-%d", count))),
		uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-file-benchmark-b-%d", count))),
	}
	for _, fileID := range fileIDs {
		if err := database.WithContext(ctx).Exec(`
INSERT INTO file(id, file_name, mime_type, file_size, extension, sha256)
VALUES (?, 'benchmark', 'application/pdf', 32, 'pdf', decode(repeat('01', 32), 'hex'))
`, fileID).Error; err != nil {
			return nil, fmt.Errorf("insert benchmark File %s: %w", fileID, err)
		}
	}
	nodes := make([]*contentv1.RichTextBlockNode, count)
	localized := make([]*contentv1.RichTextBlockLocale, count)
	nodes[0] = fileNode(blockID, fileIDs[0])
	localized[0] = fileLocale(blockID)
	for index := 1; index < count; index++ {
		paragraphID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("content-block-file-benchmark-paragraph-%d-%d", count, index)))
		nodes[index] = paragraphNode(paragraphID, nil, index, nil)
		localized[index] = paragraphLocale(paragraphID, "File benchmark padding paragraph.")
	}
	revision, err := seedRichTextDocument(ctx, database, store, documentID, richTextDocument(nodes, localized))
	if err != nil {
		return nil, fmt.Errorf("seed %d-Block File fixture: %w", count, err)
	}
	return &fileFixture{documentID: documentID, revision: revision, blockID: blockID, fileIDs: fileIDs}, nil
}

func cleanupFileFixture(ctx context.Context, database *gorm.DB, fixture *fileFixture) error {
	if err := database.WithContext(ctx).Exec(
		"DELETE FROM content_document WHERE id = ?", fixture.documentID,
	).Error; err != nil {
		return fmt.Errorf("delete File benchmark document: %w", err)
	}
	if err := database.WithContext(ctx).Exec(
		"DELETE FROM file WHERE id IN ?", fixture.fileIDs[:],
	).Error; err != nil {
		return fmt.Errorf("delete benchmark Files: %w", err)
	}
	return nil
}

func fileNode(blockID, fileID uuid.UUID) *contentv1.RichTextBlockNode {
	name := "benchmark.pdf"
	return &contentv1.RichTextBlockNode{
		Block: &contentv1.RichTextBlock{
			Id: blockID.String(),
			Value: &contentv1.RichTextBlock_File{File: &contentv1.FileBlock{Props: &contentv1.FileProps{
				Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID.String()}},
				Name:       &name,
			}}},
		},
		Placement: &contentv1.ContentBlockPlacement{},
	}
}

func fileLocale(blockID uuid.UUID) *contentv1.RichTextBlockLocale {
	alt, caption := "benchmark", "benchmark caption"
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID.String(),
		Value: &contentv1.RichTextBlockLocale_File{File: &contentv1.FileBlockLocale{Props: &contentv1.FileLocaleProps{
			Alt: &alt, Caption: &caption,
		}}},
	}
}

func sharedFileBatch(documentID, revision, blockID, fileID uuid.UUID) (contentblock.Batch, error) {
	return contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        revision.String(),
		BaseMutations: []*contentv1.RichTextBlockMutation{{
			Operation: &contentv1.RichTextBlockMutation_Upsert{Upsert: &contentv1.UpsertRichTextBlock{
				Node: fileNode(blockID, fileID),
			}},
		}},
	})
}

func prepareNestedFixture(
	ctx context.Context,
	database *gorm.DB,
	store *contentblock.Store,
) (*nestedFixture, error) {
	fixture := &nestedFixture{
		documentID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("content-block-nested-benchmark-document")),
		leftID:     uuid.NewSHA1(uuid.NameSpaceOID, []byte("content-block-nested-benchmark-left")),
		rightID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("content-block-nested-benchmark-right")),
		childID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte("content-block-nested-benchmark-child")),
	}
	nodes := []*contentv1.RichTextBlockNode{
		paragraphNode(fixture.leftID, nil, 0, nil),
		paragraphNode(fixture.rightID, nil, 1, nil),
		paragraphNode(fixture.childID, &fixture.leftID, 0, nil),
	}
	localized := []*contentv1.RichTextBlockLocale{
		paragraphLocale(fixture.leftID, "left"),
		paragraphLocale(fixture.rightID, "right"),
		paragraphLocale(fixture.childID, "child"),
	}
	revision, err := seedRichTextDocument(ctx, database, store, fixture.documentID, richTextDocument(nodes, localized))
	if err != nil {
		return nil, fmt.Errorf("seed nested fixture: %w", err)
	}
	fixture.revision = revision
	return fixture, nil
}

func insertLegacyFixtures(ctx context.Context, database *sql.DB, fixture *fixture) error {
	for _, operation := range []string{"single_edit", "full_reorder"} {
		if _, err := database.ExecContext(ctx, `
INSERT INTO benchmark_legacy_document(operation, size_blocks, revision, yjs_state, edit_hash)
VALUES ($1, $2, gen_random_uuid(), $3, 'initial')
`, operation, fixture.count, fixture.originalYJS); err != nil {
			return fmt.Errorf("insert %d-Block legacy fixture: %w", fixture.count, err)
		}
	}
	return nil
}

func measureLegacy(
	ctx context.Context,
	database *sql.DB,
	operation string,
	count int,
	payload []byte,
	iteration int,
) (sample, error) {
	started := time.Now()
	beginStarted := time.Now()
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return sample{}, err
	}
	beginDuration := time.Since(beginStarted)
	rollback := func(cause error) (sample, error) {
		_ = tx.Rollback()
		return sample{}, cause
	}
	var revision uuid.UUID
	bodySQLStarted := time.Now()
	if err := tx.QueryRowContext(ctx, `
SELECT revision
FROM benchmark_legacy_document
WHERE operation = $1 AND size_blocks = $2
FOR UPDATE
`, operation, count).Scan(&revision); err != nil {
		return rollback(err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE benchmark_legacy_document
SET revision = gen_random_uuid(), yjs_state = $3, edit_hash = $4, updated_at = clock_timestamp()
WHERE operation = $1 AND size_blocks = $2 AND revision = $5
`, operation, count, payload, fmt.Sprintf("hash-%d", iteration%2), revision)
	if err != nil {
		return rollback(err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return rollback(err)
	}
	if rows != 1 {
		return rollback(fmt.Errorf("legacy CAS updated %d rows", rows))
	}
	bodySQLDuration := time.Since(bodySQLStarted)
	commitStarted := time.Now()
	if err := tx.Commit(); err != nil {
		return sample{}, err
	}
	return sample{
		TotalMilliseconds:      milliseconds(time.Since(started)),
		BeginMilliseconds:      milliseconds(beginDuration),
		BodySQLMilliseconds:    milliseconds(bodySQLDuration),
		NonSQLBodyMilliseconds: 0,
		CommitMilliseconds:     milliseconds(time.Since(commitStarted)),
		BodyStatements:         2,
	}, nil
}

func measureStoreTransaction(
	ctx context.Context,
	database *gorm.DB,
	counter *statementCounter,
	captureRows bool,
	body func(*gorm.DB) error,
) (sample, error) {
	started := time.Now()
	beginStarted := time.Now()
	tx := database.WithContext(ctx).Begin()
	if tx.Error != nil {
		return sample{}, tx.Error
	}
	beginDuration := time.Since(beginStarted)
	var rowsReadBefore, rowsWrittenBefore int64
	if captureRows {
		var statsErr error
		rowsReadBefore, rowsWrittenBefore, statsErr = transactionRowStats(tx)
		if statsErr != nil {
			_ = tx.Rollback().Error
			return sample{}, statsErr
		}
	}
	counter.reset()
	bodyStarted := time.Now()
	if err := body(tx); err != nil {
		_ = tx.Rollback().Error
		return sample{}, err
	}
	bodyDuration := time.Since(bodyStarted)
	bodyStatements, bodySQLDuration := counter.value()
	var rowsRead, rowsWritten int64
	if captureRows {
		rowsReadAfter, rowsWrittenAfter, statsErr := transactionRowStats(tx)
		if statsErr != nil {
			_ = tx.Rollback().Error
			return sample{}, statsErr
		}
		rowsRead = rowsReadAfter - rowsReadBefore
		rowsWritten = rowsWrittenAfter - rowsWrittenBefore
	}
	commitStarted := time.Now()
	if err := tx.Commit().Error; err != nil {
		return sample{}, err
	}
	return sample{
		TotalMilliseconds:      milliseconds(time.Since(started)),
		BeginMilliseconds:      milliseconds(beginDuration),
		BodySQLMilliseconds:    milliseconds(bodySQLDuration),
		NonSQLBodyMilliseconds: milliseconds(bodyDuration - bodySQLDuration),
		CommitMilliseconds:     milliseconds(time.Since(commitStarted)),
		BodyStatements:         bodyStatements,
		RowsRead:               rowsRead,
		RowsWritten:            rowsWritten,
	}, nil
}

func transactionRowStats(tx *gorm.DB) (int64, int64, error) {
	statsDB := tx.Session(&gorm.Session{Logger: logger.Discard})
	if err := statsDB.Exec("SELECT pg_stat_clear_snapshot()").Error; err != nil {
		return 0, 0, fmt.Errorf("clear transaction row statistics snapshot: %w", err)
	}
	var stats struct {
		RowsRead    int64 `gorm:"column:rows_read"`
		RowsWritten int64 `gorm:"column:rows_written"`
	}
	if err := statsDB.Raw(`
SELECT COALESCE(sum(seq_tup_read + idx_tup_fetch), 0)::bigint AS rows_read,
       COALESCE(sum(n_tup_ins + n_tup_upd + n_tup_del), 0)::bigint AS rows_written
FROM pg_stat_xact_user_tables
WHERE schemaname = 'public'
  AND relname IN ('content_document', 'content_block', 'content_block_locale',
                  'content_block_attachment', 'file')
`).Scan(&stats).Error; err != nil {
		return 0, 0, fmt.Errorf("read transaction row statistics: %w", err)
	}
	return stats.RowsRead, stats.RowsWritten, nil
}

func measure(repetitions, warmups int, operation func(int) (sample, error)) ([]sample, error) {
	for iteration := range warmups {
		if _, err := operation(iteration); err != nil {
			return nil, err
		}
	}
	samples := make([]sample, 0, repetitions)
	for iteration := range repetitions {
		value, err := operation(iteration + warmups)
		if err != nil {
			return nil, err
		}
		samples = append(samples, value)
	}
	return samples, nil
}

func summarize(samples []sample, repetitions int, rowProbe sample) operationSummary {
	totals := make([]float64, 0, len(samples))
	begins := make([]float64, 0, len(samples))
	bodySQL := make([]float64, 0, len(samples))
	nonSQLBody := make([]float64, 0, len(samples))
	commits := make([]float64, 0, len(samples))
	statementSet := make(map[int]struct{})
	for _, value := range samples {
		totals = append(totals, value.TotalMilliseconds)
		begins = append(begins, value.BeginMilliseconds)
		bodySQL = append(bodySQL, value.BodySQLMilliseconds)
		nonSQLBody = append(nonSQLBody, value.NonSQLBodyMilliseconds)
		commits = append(commits, value.CommitMilliseconds)
		statementSet[value.BodyStatements] = struct{}{}
	}
	statements := make([]int, 0, len(statementSet))
	for value := range statementSet {
		statements = append(statements, value)
	}
	sort.Ints(statements)
	return operationSummary{
		Repetitions: repetitions, BodyStatements: statements,
		TransactionMilliseconds: durationSummary{P50: percentile(totals, 0.50), P95: percentile(totals, 0.95)},
		BeginMilliseconds:       durationSummary{P50: percentile(begins, 0.50), P95: percentile(begins, 0.95)},
		BodySQLMilliseconds:     durationSummary{P50: percentile(bodySQL, 0.50), P95: percentile(bodySQL, 0.95)},
		NonSQLBodyMilliseconds:  durationSummary{P50: percentile(nonSQLBody, 0.50), P95: percentile(nonSQLBody, 0.95)},
		CommitMilliseconds:      durationSummary{P50: percentile(commits, 0.50), P95: percentile(commits, 0.95)},
		RowsRead:                rowProbe.RowsRead, RowsWritten: rowProbe.RowsWritten,
	}
}

func milliseconds(value time.Duration) float64 {
	return float64(value.Microseconds()) / 1_000
}

func percentile(values []float64, probability float64) float64 {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(float64(len(sorted))*probability+0.999999999) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}

func envPositiveInt(name string, fallback int) int {
	var value int
	if _, err := fmt.Sscanf(os.Getenv(name), "%d", &value); err == nil && value > 0 {
		return value
	}
	return fallback
}
