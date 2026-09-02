package aidocumentadapter

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func TestNewWorkRegistrationRejectsMissingDomainService(t *testing.T) {
	_, err := NewWorkRegistration(nil)
	require.ErrorContains(t, err, "dependencies are required")
}

func TestWorkPortProjectsMetadataAndGeneratedBlocksWithoutFallback(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	port := &workPort{codec: codec, catalog: workCatalog(codec)}
	blockID := uuid.NewString()
	empty := ""
	state := workdomain.AIDocumentState{
		SourceLocale: "en", Locale: "ko", LocaleExists: true, Title: &empty,
		DocumentRevision: uuid.NewString(), TargetRevision: stringPointer("work-target-1"),
		Snapshot: contentblock.Snapshot{Document: contentblock.Document{ID: uuid.New(), Revision: uuid.New()}},
		Document: &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: codec.Catalog().Fingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK,
			Locale:                  "ko",
			Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
				Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
				Placement: &contentv1.ContentBlockPlacement{},
			}}},
			LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
		},
	}
	identity := core.DocumentIdentity{Domain: core.DomainWork, Reference: core.DocumentReference(uuid.NewString())}

	document, err := port.project(identity, "ko", state)
	require.NoError(t, err)
	require.True(t, document.LocaleExists)
	require.Len(t, document.Nodes, 2)
	require.Equal(t, workMetadataBlockID, document.Nodes[0].ID)
	require.Equal(t, core.Text(""), document.Nodes[0].Localized[0].Value)
	require.Empty(t, document.Nodes[1].Localized, "missing target Block must not source-fallback")
}

func TestCompileWorkMetadataPreservesExplicitEmptyAndRejectsUnset(t *testing.T) {
	empty := core.Text("")
	patch := workdomain.AIDocumentMetadataPatch{}
	handled, issue := compileWorkMetadataOperation(&patch, core.SetFieldOperation(workMetadataBlockID, workTitleField, empty), 0)
	require.True(t, handled)
	require.Nil(t, issue)
	require.True(t, patch.SetTitle)
	require.NotNil(t, patch.Title)
	require.Empty(t, *patch.Title)

	handled, issue = compileWorkMetadataOperation(&patch, core.UnsetFieldOperation(workMetadataBlockID, workTitleField), 1)
	require.True(t, handled)
	require.NotNil(t, issue)
	require.Equal(t, core.IssueInvalidOperation, issue.Code)
}

func TestWorkCompileDoesNotCreateMissingTargetForUnset(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	blockID := uuid.NewString()
	revision := uuid.New()
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: codec.Catalog().Fingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, Locale: "ko",
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block:     &contentv1.RichTextBlock{Id: blockID, Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}}},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: "ko"},
	}
	state := workdomain.AIDocumentState{
		SourceLocale: "en", Locale: "ko", LocaleExists: false,
		Snapshot: contentblock.Snapshot{Document: contentblock.Document{ID: uuid.New(), Revision: revision}},
		Document: document,
	}
	loaded := core.Document{
		Identity:         core.DocumentIdentity{Domain: core.DomainWork, Reference: core.DocumentReference(uuid.NewString())},
		DocumentRevision: core.Revision(revision.String()), SourceLocale: "en", Locale: "ko", LocaleExists: false,
	}
	port := &workPort{codec: codec}
	mutation, issues, err := port.compile(state, uuid.New(), loaded, []core.Operation{
		core.UnsetFieldOperation(core.BlockID(blockID), "content"),
	})
	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Contains(t, issues[0].Message, "explicit empty")
	require.False(t, mutation.Metadata.EnsureLocale)
	require.Empty(t, mutation.Batch.LocaleGroups)
	require.Empty(t, mutation.Batch.Upserts)
	require.Empty(t, mutation.Batch.Deletes)
}
