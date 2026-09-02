package programevent

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInitializeProgramEventBlockTranslationState(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:program-event-block-translation?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE program_event (
		id TEXT PRIMARY KEY,
		source_locale TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE program_event_translation (
		entity_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		summary TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (entity_id, locale)
	)`).Error)

	ctx := context.Background()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	summary := "Source summary"
	eventID := "7a5deaa0-f409-4d6c-a1d6-2766f79c69fb"
	require.NoError(t, db.Exec(`INSERT INTO program_event (id, source_locale) VALUES (?, ?)`, eventID, "en").Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return initializeProgramEventBlockTranslationSource(
			ctx,
			tx,
			eventID,
			"en",
			&summary,
			now,
		)
	}))

	state, err := loadProgramEventSourceLocale(ctx, db, eventID, false)
	require.NoError(t, err)
	require.Equal(t, "en", state.SourceLocale)

	var source model.ProgramEventTranslation
	require.NoError(t, db.Take(
		&source,
		"entity_id = ? AND locale = ?",
		eventID,
		state.SourceLocale,
	).Error)
	require.Equal(t, &summary, source.Summary)

}

func TestLoadProgramEventLocalesRendersTypedBlockAuthority(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:program-event-locale-projection?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE program_event (
		id TEXT PRIMARY KEY,
		source_locale TEXT NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE program_event_translation (
		entity_id TEXT NOT NULL,
		locale TEXT NOT NULL,
		summary TEXT,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY (entity_id, locale)
	)`).Error)

	eventID := uuid.NewString()
	now := time.Date(2026, time.August, 23, 18, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`INSERT INTO program_event (id, source_locale) VALUES (?, 'en')`, eventID).Error)
	require.NoError(t, db.Exec(`INSERT INTO program_event_translation (
		entity_id, locale, summary, created_at, updated_at
	) VALUES (?, 'en', 'Source summary', ?, ?), (?, 'ko', '번역 요약', ?, ?)`, eventID, now, now, eventID, now, now).Error)

	blockID := uuid.NewString()
	document := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		SourceLocale:            "en",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlays: []*contentv1.RichTextLocaleOverlay{
			programEventParagraphOverlay(blockID, "en", "Source body"),
			programEventParagraphOverlay(blockID, "ko", "번역 본문"),
		},
	}
	documentID := uuid.New()
	revision := uuid.New()
	replacement, err := contentblock.ReplaceFromRichTextProto(documentID, revision, document)
	require.NoError(t, err)
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: documentID, Profile: programEventContentProfile, Revision: revision},
		SourceLocale: "en", Blocks: replacement.Blocks, LocaleOverlays: replacement.LocaleOverlays,
	}

	locales, err := loadProgramEventLocales(t.Context(), db, eventID, snapshot, nil)
	require.NoError(t, err)
	require.Len(t, locales, 2)
	require.Equal(t, "en", locales[0].Locale)
	require.Equal(t, "<p>Source body</p>", locales[0].GetContentHtml())
	require.Equal(t, "Source body", locales[0].GetContentText())
	require.Equal(t, "ko", locales[1].Locale)
	require.Equal(t, "<p>번역 본문</p>", locales[1].GetContentHtml())
	require.Equal(t, "번역 본문", locales[1].GetContentText())
}

func programEventParagraphOverlay(blockID string, locale string, text string) *contentv1.RichTextLocaleOverlay {
	return &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{
				Text: &contentv1.RichTextStyledText{Text: text},
			}}},
		}},
	}}}
}
