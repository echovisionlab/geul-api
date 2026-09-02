//go:build integration

package contentblock_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/testutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

type statementCountLogger struct {
	count int
}

func (counter *statementCountLogger) LogMode(logger.LogLevel) logger.Interface { return counter }
func (*statementCountLogger) Info(context.Context, string, ...any)             {}
func (*statementCountLogger) Warn(context.Context, string, ...any)             {}
func (*statementCountLogger) Error(context.Context, string, ...any)            {}
func (counter *statementCountLogger) Trace(
	context.Context,
	time.Time,
	func() (string, int64),
	error,
) {
	counter.count++
}

type postgresPersistenceReuseAuthorizer struct{}

func (postgresPersistenceReuseAuthorizer) AuthorizeFileReuse(
	context.Context,
	*gorm.DB,
	contentblock.Document,
	contentblock.FullBlock,
	contentblock.FileReference,
	contentblock.File,
) error {
	return nil
}

func TestPostgresReplaceSnapshotPersistsGeneratedRichTextJSONB(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	store, err := contentblock.NewGeneratedStore(postgresPersistenceReuseAuthorizer{})
	require.NoError(t, err)

	documentID := uuid.New()
	var created contentblock.Snapshot
	err = postgres.DB.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			ID:           documentID,
			Profile:      "post",
			SourceLocale: "en",
		})
		return createErr
	})
	require.NoError(t, err)

	blockID := uuid.New()
	replace, err := contentblock.ReplaceFromRichTextProto(
		documentID,
		created.Document.Revision,
		postgresRichTextDocument(blockID, "PostgreSQL JSONB"),
	)
	require.NoError(t, err)

	var result contentblock.Result
	err = postgres.DB.Transaction(func(tx *gorm.DB) error {
		var replaceErr error
		result, replaceErr = store.ReplaceSnapshot(
			t.Context(),
			tx,
			replace,
			func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
				return contentblock.DomainContext{SourceLocale: "en"}, nil
			},
		)
		return replaceErr
	})
	require.NoError(t, err)
	require.True(t, result.Changed)

	loaded, err := store.LoadSnapshot(t.Context(), postgres.DB, documentID, "en")
	require.NoError(t, err)
	require.Equal(t, documentID, loaded.Document.ID)
	require.Equal(t, result.DocumentRevision, loaded.Document.Revision)
	require.NotEmpty(t, loaded.SnapshotDigest)
	require.Len(t, loaded.Blocks, 1)
	require.Len(t, loaded.LocaleOverlays, 1)
	require.Len(t, loaded.LocaleOverlays[0].Blocks, 1)

	materialized, err := contentblock.SnapshotToLocalizedRichTextDocument(loaded, "en")
	require.NoError(t, err)
	require.Equal(
		t,
		"PostgreSQL JSONB",
		materialized.GetLocaleOverlay().GetBlocks()[0].GetParagraph().GetContent()[0].GetText().GetText(),
	)
}

func TestPostgresLocaleEditUsesOneCASStatementAndRollsBackInvalidPayload(t *testing.T) {
	postgres := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
	store, err := contentblock.NewGeneratedStore(postgresPersistenceReuseAuthorizer{})
	require.NoError(t, err)

	documentID := uuid.New()
	blockID := uuid.New()
	var created contentblock.Snapshot
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		var createErr error
		created, createErr = store.CreateDocument(t.Context(), tx, contentblock.CreateInput{
			ID: documentID, Profile: "post", SourceLocale: "en",
		})
		return createErr
	}))
	replace, err := contentblock.ReplaceFromRichTextProto(
		documentID,
		created.Document.Revision,
		postgresRichTextDocument(blockID, "before"),
	)
	require.NoError(t, err)
	var seeded contentblock.Result
	require.NoError(t, postgres.DB.Transaction(func(tx *gorm.DB) error {
		var replaceErr error
		seeded, replaceErr = store.ReplaceSnapshot(t.Context(), tx, replace, postgresFence("en"))
		return replaceErr
	}))

	localized := postgresParagraphLocaleJSON(t, "after")
	batch := postgresParagraphLocaleMutation(t, documentID, seeded.DocumentRevision, blockID, "fr", "after")
	counter := &statementCountLogger{}
	measuredDB := postgres.DB.Session(&gorm.Session{Logger: counter})
	var applied contentblock.Result
	require.NoError(t, measuredDB.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		applied, applyErr = store.ApplyBatch(t.Context(), tx, batch, postgresFence("en"))
		return applyErr
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted}))
	require.Equal(t, 1, counter.count)
	require.True(t, applied.Changed)
	require.False(t, applied.TranslationSourceChanged)
	require.Equal(t, []string{"fr"}, applied.ChangedLocales)
	require.NotEqual(t, batch.ExpectedRevision, applied.DocumentRevision)

	var stored blockLocaleProbe
	require.NoError(t, postgres.DB.Raw(`
SELECT block_id, locale, localized_data
FROM content_block_locale
WHERE block_id = ? AND locale = 'fr'`, blockID).Scan(&stored).Error)
	require.JSONEq(t, string(localized), string(stored.LocalizedData))

	noOp := batch
	noOp.ExpectedRevision = applied.DocumentRevision
	counter.count = 0
	var unchanged contentblock.Result
	require.NoError(t, measuredDB.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		unchanged, applyErr = store.ApplyBatch(t.Context(), tx, noOp, postgresFence("en"))
		return applyErr
	}))
	require.Equal(t, 1, counter.count)
	require.False(t, unchanged.Changed)
	require.Equal(t, applied.DocumentRevision, unchanged.DocumentRevision)

	require.NoError(t, postgres.DB.Exec(`
UPDATE content_block
SET kind = 'heading'
WHERE id = ?`, blockID).Error)
	invalid := postgresParagraphLocaleMutation(
		t,
		documentID,
		applied.DocumentRevision,
		blockID,
		"fr",
		"must roll back",
	)
	counter.count = 0
	err = measuredDB.Transaction(func(tx *gorm.DB) error {
		_, applyErr := store.ApplyBatch(t.Context(), tx, invalid, postgresFence("en"))
		return applyErr
	})
	require.ErrorIs(t, err, contentblock.ErrInvalidMutation)
	require.Equal(t, 1, counter.count)

	var persistedDocument struct {
		Revision uuid.UUID
	}
	require.NoError(t, postgres.DB.Raw(`SELECT revision FROM content_document WHERE id = ?`, documentID).Scan(&persistedDocument).Error)
	require.Equal(t, applied.DocumentRevision, persistedDocument.Revision)
	require.NoError(t, postgres.DB.Raw(`
SELECT block_id, locale, localized_data
FROM content_block_locale
WHERE block_id = ? AND locale = 'fr'`, blockID).Scan(&stored).Error)
	require.JSONEq(t, string(localized), string(stored.LocalizedData))
}

type blockLocaleProbe struct {
	BlockID       uuid.UUID
	Locale        string
	LocalizedData []byte
}

func postgresFence(sourceLocale string) contentblock.DomainFence {
	return func(context.Context, *gorm.DB, uuid.UUID) (contentblock.DomainContext, error) {
		return contentblock.DomainContext{SourceLocale: sourceLocale}, nil
	}
}

func postgresParagraphLocaleJSON(t *testing.T, text string) json.RawMessage {
	t.Helper()
	value, err := protojson.Marshal(&contentv1.RichTextBlockLocale{
		BlockId: uuid.NewString(),
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		}},
	})
	require.NoError(t, err)
	var object map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(value, &object))
	delete(object, "blockId")
	value, err = json.Marshal(object)
	require.NoError(t, err)
	return value
}

func postgresParagraphLocaleMutation(
	t *testing.T,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	blockID uuid.UUID,
	locale string,
	text string,
) contentblock.Batch {
	t.Helper()
	batch, err := contentblock.BatchFromRichTextSystemProto(documentID, &contentv1.RichTextBlockMutationBatch{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		ExpectedRevision:        expectedRevision.String(),
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
	})
	require.NoError(t, err)
	return batch
}

func postgresRichTextDocument(blockID uuid.UUID, text string) *contentv1.RichTextDocument {
	return &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID.String(), Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{{
			Locale: "en",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: blockID.String(),
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
						Text: &contentv1.RichTextStyledText{Text: text},
					}}},
				}},
			}},
		}},
	}
}
