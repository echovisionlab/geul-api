package translationadapter

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestPostInterchangeExportProjectionPreservesStableBlockIdentityAndExplicitEmpty(t *testing.T) {
	blockID := uuid.NewString()
	source := postInterchangeSourceDocument(
		[]string{blockID},
		map[string]string{blockID: "Source paragraph"},
	)
	plan := requirePostInterchangePlan(t, source)
	target := localizedInterchangeDocument(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		"ko",
		source.ContentBlockDocument.GetBase(),
		map[string]string{blockID: ""},
	)

	targets, err := projectBlockInterchangeTargets(plan, target)
	require.NoError(t, err)
	handle := requireBlockInterchangeHandle(t, plan, blockID)
	projected, ok := targets[handle]
	require.True(t, ok, "explicit empty target must remain present during export")
	require.Empty(t, projected.TranslatedText)
	require.Equal(t, handle, projected.UnitID)
}

func TestPostInterchangeImportPatchPreservesUnselectedTargetAndAppliesExplicitEmpty(t *testing.T) {
	firstID := uuid.NewString()
	secondID := uuid.NewString()
	source := postInterchangeSourceDocument(
		[]string{firstID, secondID},
		map[string]string{firstID: "Source first", secondID: "Source second"},
	)
	plan := requirePostInterchangePlan(t, source)
	current := localizedInterchangeDocument(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		"ko",
		source.ContentBlockDocument.GetBase(),
		map[string]string{firstID: "Target first", secondID: "Target second"},
	)
	firstHandle := requireBlockInterchangeHandle(t, plan, firstID)
	secondHandle := requireBlockInterchangeHandle(t, plan, secondID)
	currentTargets, err := projectBlockInterchangeTargets(plan, current)
	require.NoError(t, err)
	command := application.TranslationInterchangeApply{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
		Source: source, Plan: plan,
		Targets: map[string]core.UnitResult{
			firstHandle: {UnitID: firstHandle, TranslatedText: ""},
		},
		UnitHandles: []string{firstHandle},
	}
	targets := interchangeCandidateTargets(command.Mode, currentTargets, command.Targets)
	candidate, err := buildPostInterchangeCandidate(command, postInterchangeTarget{
		state: application.TranslationInterchangeTargetState{
			Exists: true, Revision: "revision-a", Targets: currentTargets,
		},
		localized: current,
	})
	require.NoError(t, err)
	require.Len(t, candidate.ContentBlockLocaleOverlay.GetBlocks(), 1, "PATCH writes only touched Blocks")
	require.Equal(t, firstID, candidate.ContentBlockLocaleOverlay.GetBlocks()[0].GetBlockId())
	require.Empty(t, paragraphInterchangeText(candidate.ContentBlockLocaleOverlay.GetBlocks()[0]))
	require.Nil(t, candidate.Title, "PATCH must not synthesize a missing target title from source")
	require.Equal(t, "Target second", targets[secondHandle].TranslatedText, "unselected target remains authoritative")
}

func TestPostInterchangeExportIgnoresTargetBlockDeletedFromCurrentSource(t *testing.T) {
	currentID := uuid.NewString()
	deletedID := uuid.NewString()
	source := postInterchangeSourceDocument(
		[]string{currentID},
		map[string]string{currentID: "Current source"},
	)
	plan := requirePostInterchangePlan(t, source)
	target := localizedInterchangeDocument(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		"ko",
		source.ContentBlockDocument.GetBase(),
		map[string]string{currentID: "Current target", deletedID: "Deleted target"},
	)

	targets, err := projectBlockInterchangeTargets(plan, target)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	for _, target := range targets {
		require.Equal(t, currentID, planUnit(t, plan, target.UnitID).ContainerID)
	}
}

func TestPostInterchangeImportReplacePreservesExplicitEmptyEntityTarget(t *testing.T) {
	blockID := uuid.NewString()
	source := postInterchangeSourceDocument(
		[]string{blockID},
		map[string]string{blockID: "Source paragraph"},
	)
	plan := requirePostInterchangePlan(t, source)
	blockHandle := requireBlockInterchangeHandle(t, plan, blockID)
	command := application.TranslationInterchangeApply{
		EntityType: "post", EntityID: "post-1", SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: source, Plan: plan,
		Targets: map[string]core.UnitResult{
			"entity:title": {UnitID: "entity:title", TranslatedText: ""},
			blockHandle:    {UnitID: blockHandle, TranslatedText: "Target paragraph"},
		},
	}
	candidate, err := buildPostInterchangeCandidate(command, postInterchangeTarget{})
	require.NoError(t, err)
	require.NotNil(t, candidate.Title)
	require.Empty(t, *candidate.Title)
	require.Equal(t, "Target paragraph", paragraphInterchangeText(candidate.ContentBlockLocaleOverlay.GetBlocks()[0]))
}

func TestPostInterchangeValidateRejectsIdentityAndRevisionMismatch(t *testing.T) {
	source := postInterchangeSourceDocument(
		[]string{uuid.NewString()},
		map[string]string{},
	)
	plan := requirePostInterchangePlan(t, source)
	command := application.TranslationInterchangeApply{
		EntityType: "post", EntityID: "other-post", SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: source, Plan: plan,
	}
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(validateBlockInterchangeApply(command, "post")))

	revision := "revision-a"
	require.Equal(t, connect.CodeAborted, connect.CodeOf(requireTranslationInterchangeRevision(
		application.TranslationInterchangeTargetState{Exists: true, Revision: "revision-b"},
		&revision,
	)))
	require.Equal(t, connect.CodeAborted, connect.CodeOf(requireTranslationInterchangeRevision(
		application.TranslationInterchangeTargetState{Exists: true, Revision: "revision-a"},
		nil,
	)))
}

func requirePostInterchangePlan(t *testing.T, source *core.SourceDocument) *core.ExtractionPlan {
	t.Helper()
	plan, err := core.BuildRichTextExtractionPlan(
		&model.TranslationJob{
			EntityType: "post", EntityID: "post-1", SourceLocale: "en", TargetLocale: "ko",
		},
		source,
		core.RichTextDocumentFields{Title: true, Summary: true},
	)
	require.NoError(t, err)
	return plan
}

func postInterchangeSourceDocument(blockIDs []string, values map[string]string) *core.SourceDocument {
	document := localizedInterchangeDocument(
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST,
		"en",
		interchangeBaseGraph(blockIDs),
		values,
	)
	return &core.SourceDocument{
		Title: "Source title", ContentDocumentRevision: uuid.NewString(),
		ContentBlockDocument: document,
	}
}

func localizedInterchangeDocument(
	profile contentv1.RichTextProfile,
	locale string,
	base *contentv1.RichTextBlockGraph,
	values map[string]string,
) *contentv1.LocalizedRichTextDocument {
	blocks := make([]*contentv1.RichTextBlockLocale, 0, len(values))
	for _, node := range base.GetNodes() {
		blockID := node.GetBlock().GetId()
		value, ok := values[blockID]
		if !ok {
			continue
		}
		blocks = append(blocks, paragraphInterchangeBlock(blockID, value))
	}
	for blockID, value := range values {
		found := false
		for _, block := range blocks {
			found = found || block.GetBlockId() == blockID
		}
		if !found {
			blocks = append(blocks, paragraphInterchangeBlock(blockID, value))
		}
	}
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 profile, Locale: locale,
		Base:          proto.Clone(base).(*contentv1.RichTextBlockGraph),
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: blocks},
	}
}

func interchangeBaseGraph(blockIDs []string) *contentv1.RichTextBlockGraph {
	graph := &contentv1.RichTextBlockGraph{}
	for index, blockID := range blockIDs {
		graph.Nodes = append(graph.Nodes, &contentv1.RichTextBlockNode{
			Block: &contentv1.RichTextBlock{
				Id: blockID,
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{
					Props: &contentv1.ParagraphProps{},
				}},
			},
			Placement: &contentv1.ContentBlockPlacement{Index: uint32(index)},
		})
	}
	return graph
}

func paragraphInterchangeBlock(blockID string, text string) *contentv1.RichTextBlockLocale {
	return &contentv1.RichTextBlockLocale{
		BlockId: blockID,
		Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
			Props: &contentv1.ParagraphLocaleProps{},
			Content: []*contentv1.RichTextInline{{
				Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: text}},
			}},
		}},
	}
}

func paragraphInterchangeText(block *contentv1.RichTextBlockLocale) string {
	return block.GetParagraph().GetContent()[0].GetText().GetText()
}

func requireBlockInterchangeHandle(t *testing.T, plan *core.ExtractionPlan, blockID string) string {
	t.Helper()
	for _, unit := range plan.Units {
		if unit.ContainerType == core.ContainerTypeBlock && unit.ContainerID == blockID {
			return unit.UnitID
		}
	}
	t.Fatalf("no Block interchange unit for %s", blockID)
	return ""
}

func planUnit(t *testing.T, plan *core.ExtractionPlan, handle string) core.Unit {
	t.Helper()
	for _, unit := range plan.Units {
		if unit.UnitID == handle {
			return unit
		}
	}
	t.Fatalf("no interchange unit %s", handle)
	return core.Unit{}
}
