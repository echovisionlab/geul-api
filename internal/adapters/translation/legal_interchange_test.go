package translationadapter

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type legalInterchangeDomainStub struct {
	current  legal.AIDocument
	accepted legal.AIDocument
	result   legal.AIDocumentMutationResult
	mutation legal.AIDocumentMutation
	applied  bool
}

func (s *legalInterchangeDomainStub) LoadTranslationInterchangeAIDocumentWithDB(
	context.Context,
	*gorm.DB,
	string,
	string,
	string,
) (legal.AIDocument, error) {
	if s.applied {
		return s.accepted, nil
	}
	return s.current, nil
}

func (s *legalInterchangeDomainStub) ExecuteTranslationInterchangeMutationWithDB(
	_ context.Context,
	_ *gorm.DB,
	_ string,
	_ string,
	_ string,
	compiler legal.AIDocumentMutationCompiler,
) (legal.AIDocumentMutationResult, error) {
	mutation, err := compiler(s.current)
	if err != nil {
		return legal.AIDocumentMutationResult{}, err
	}
	s.mutation = mutation
	s.applied = true
	return s.result, nil
}

func TestLegalInterchangeProjectionDistinguishesAbsentAndExplicitEmptyTargets(t *testing.T) {
	fixture := newLegalInterchangeFixture(t, "privacy")

	absent, err := projectLegalInterchangeTarget(fixture.raw(false, false, nil), fixture.plan)
	require.NoError(t, err)
	require.False(t, absent.state.Exists)
	require.Empty(t, absent.state.Revision)
	require.Empty(t, absent.state.Targets)

	empty := ""
	present, err := projectLegalInterchangeTarget(fixture.raw(true, true, &empty), fixture.plan)
	require.NoError(t, err)
	require.True(t, present.state.Exists)
	require.NotEmpty(t, present.state.Revision)
	require.Contains(t, present.state.Targets, "entity:title")
	require.Equal(t, "", present.state.Targets["entity:title"].TranslatedText)
	bodyHandle := fixture.bodyHandle(t)
	require.Contains(t, present.state.Targets, bodyHandle)
	require.Equal(t, "", present.state.Targets[bodyHandle].TranslatedText)

	sparse, err := projectLegalInterchangeTarget(fixture.raw(true, false, nil), fixture.plan)
	require.NoError(t, err)
	require.True(t, sparse.state.Exists)
	require.NotContains(t, sparse.state.Targets, "entity:title")
	require.NotContains(t, sparse.state.Targets, bodyHandle)
}

func TestLegalInterchangeCreateUsesOnlyTargetLocaleAndCanRecreateAfterDelete(t *testing.T) {
	fixture := newLegalInterchangeFixture(t, "terms")
	empty := ""
	accepted := fixture.raw(true, true, &empty)
	acceptedTarget, err := projectLegalInterchangeTarget(accepted, fixture.plan)
	require.NoError(t, err)
	domain := &legalInterchangeDomainStub{
		current:  fixture.raw(false, false, nil),
		accepted: accepted,
		result: legal.AIDocumentMutationResult{
			Revision: fixture.revision, TargetRevision: &acceptedTarget.state.Revision, Changed: true,
		},
	}
	port := NewLegalInterchange(domain)
	targets := make(map[string]core.UnitResult, len(fixture.plan.Units))
	handles := make([]string, 0, len(fixture.plan.Units))
	for _, unit := range fixture.plan.Units {
		targets[unit.UnitID] = core.UnitResult{UnitID: unit.UnitID, TranslatedText: ""}
		handles = append(handles, unit.UnitID)
	}
	result, err := port.ApplyTranslationInterchange(
		context.Background(),
		&gorm.DB{},
		&contentblock.Store{},
		application.TranslationInterchangeApply{
			EntityType: fixture.entityType, EntityID: fixture.entityID,
			SourceLocale: "en", TargetLocale: "ko",
			Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
			Source: fixture.source, Plan: fixture.plan, Targets: targets, UnitHandles: handles,
			Now: time.Now().UTC(),
		},
	)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.NotEmpty(t, result.Revision)
	require.ElementsMatch(t, handles, result.AffectedUnitHandles)
	require.Equal(t, legal.AITranslationCreate, domain.mutation.Translation)
	require.True(t, domain.mutation.AuthoritativeTargetReplacement)
	require.True(t, domain.mutation.SetTitle)
	require.NotNil(t, domain.mutation.Title)
	require.Equal(t, "", *domain.mutation.Title)
	require.NotNil(t, domain.mutation.Content)
	require.Empty(t, domain.mutation.Content.Upserts)
	require.Empty(t, domain.mutation.Content.Deletes)
	require.Empty(t, domain.mutation.Content.Reorders)
	require.Len(t, domain.mutation.Content.LocaleGroups, 1)
	require.Equal(t, "ko", domain.mutation.Content.LocaleGroups[0].Locale)
}

func TestLegalInterchangeReplaceDeletesOmittedCurrentTargetWhilePatchDoesNot(t *testing.T) {
	fixture := newLegalInterchangeFixture(t, "privacy")
	title := "Current title"
	current, err := projectLegalInterchangeTarget(fixture.raw(true, true, &title), fixture.plan)
	require.NoError(t, err)

	omittedBlockID := uuid.NewString()
	omittedBase := &contentv1.RichTextBlockNode{
		Block: &contentv1.RichTextBlock{
			Id: omittedBlockID,
			Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			},
		},
		Placement: &contentv1.ContentBlockPlacement{Index: 1},
	}
	fixture.source.ContentBlockDocument.Base.Nodes = append(
		fixture.source.ContentBlockDocument.Base.Nodes, omittedBase,
	)
	current.document.Base.Nodes = append(current.document.Base.Nodes, omittedBase)
	current.document.LocaleOverlay.Blocks = append(
		current.document.LocaleOverlay.Blocks,
		&contentv1.RichTextBlockLocale{
			BlockId: omittedBlockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{
				Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{
							Text: &contentv1.RichTextStyledText{Text: "obsolete target"},
						},
					}},
				},
			},
		},
	)

	targets := make(map[string]core.UnitResult, len(fixture.plan.Units))
	handles := make([]string, 0, len(fixture.plan.Units))
	for _, unit := range fixture.plan.Units {
		targets[unit.UnitID] = core.UnitResult{UnitID: unit.UnitID, TranslatedText: "replacement"}
		handles = append(handles, unit.UnitID)
	}
	command := application.TranslationInterchangeApply{
		EntityType: fixture.entityType, EntityID: fixture.entityID,
		SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: fixture.source, Plan: fixture.plan, Targets: targets, UnitHandles: handles,
	}

	replacement, err := buildLegalInterchangeMutation(command, current)
	require.NoError(t, err)
	require.True(t, replacement.AuthoritativeTargetReplacement)
	require.NotNil(t, replacement.Content)
	require.Equal(t, []uuid.UUID{uuid.MustParse(omittedBlockID)}, replacement.Content.LocaleGroups[0].Deletes)

	command.Mode = managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH
	patch, err := buildLegalInterchangeMutation(command, current)
	require.NoError(t, err)
	require.False(t, patch.AuthoritativeTargetReplacement)
	if patch.Content != nil {
		require.Empty(t, patch.Content.LocaleGroups[0].Deletes)
	}
}

func TestLegalInterchangeRejectsStaleTargetRevisionInsideCompiler(t *testing.T) {
	fixture := newLegalInterchangeFixture(t, "privacy")
	empty := ""
	domain := &legalInterchangeDomainStub{current: fixture.raw(true, true, &empty)}
	port := NewLegalInterchange(domain)
	title := "changed"
	stale := "tr1_stale"
	_, err := port.ApplyTranslationInterchange(
		context.Background(),
		&gorm.DB{},
		&contentblock.Store{},
		application.TranslationInterchangeApply{
			EntityType: fixture.entityType, EntityID: fixture.entityID,
			SourceLocale: "en", TargetLocale: "ko",
			Mode:             managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_PATCH,
			ExpectedRevision: &stale,
			Source:           fixture.source, Plan: fixture.plan,
			Targets: map[string]core.UnitResult{
				"entity:title": {UnitID: "entity:title", TranslatedText: title},
			},
			UnitHandles: []string{"entity:title"},
			Now:         time.Now().UTC(),
		},
	)
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	require.False(t, domain.applied)
}

func TestLegalInterchangeReplaceRequiresCompleteCurrentManifest(t *testing.T) {
	fixture := newLegalInterchangeFixture(t, "terms")
	command := application.TranslationInterchangeApply{
		EntityType: fixture.entityType, EntityID: fixture.entityID,
		SourceLocale: "en", TargetLocale: "ko",
		Mode:   managev1.TranslationInterchangeMode_TRANSLATION_INTERCHANGE_MODE_REPLACE,
		Source: fixture.source, Plan: fixture.plan,
		Targets: map[string]core.UnitResult{
			"entity:title": {UnitID: "entity:title", TranslatedText: "only title"},
		},
		UnitHandles: []string{"entity:title"},
	}
	require.ErrorContains(t, validateLegalInterchangeApply(command), "complete current")
}

func TestValidateLegalInterchangeTargetOnlyBatchRejectsBaseStructure(t *testing.T) {
	err := validateLegalInterchangeTargetOnlyBatch(contentblock.Batch{
		Upserts:      []contentblock.BaseBlock{{}},
		LocaleGroups: []contentblock.LocaleMutationGroup{{Locale: "ko"}},
	}, "ko")
	require.ErrorContains(t, err, "target locale")
}

type legalInterchangeFixture struct {
	entityType string
	entityID   string
	documentID uuid.UUID
	revision   string
	memberID   string
	source     *core.SourceDocument
	plan       *core.ExtractionPlan
	base       *contentv1.RichTextBlockGraph
	sourceBody *contentv1.RichTextLocaleOverlay
}

func newLegalInterchangeFixture(t *testing.T, entityType string) legalInterchangeFixture {
	t.Helper()
	entityID := uuid.NewString()
	blockID := uuid.NewString()
	base := &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
		Block: &contentv1.RichTextBlock{
			Id: blockID,
			Value: &contentv1.RichTextBlock_Paragraph{
				Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}},
			},
		},
		Placement: &contentv1.ContentBlockPlacement{},
	}}}
	sourceOverlay := &contentv1.RichTextLocaleOverlay{
		Locale: "en",
		Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: blockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{
				Paragraph: &contentv1.ParagraphBlockLocale{
					Props: &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{
						Value: &contentv1.RichTextInline_Text{
							Text: &contentv1.RichTextStyledText{Text: "Source body"},
						},
					}},
				},
			},
		}},
	}
	revision := uuid.NewString()
	source := &core.SourceDocument{
		Title:                   "Source title",
		ContentDocumentRevision: revision,
		ContentBlockDocument: &contentv1.LocalizedRichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
			Locale:                  "en",
			Base:                    base,
			LocaleOverlay:           sourceOverlay,
		},
	}
	plan, err := legal.BuildTranslationExtractionPlan(&model.TranslationJob{
		EntityType: entityType, EntityID: entityID, SourceLocale: "en", TargetLocale: "ko",
	}, source)
	require.NoError(t, err)
	return legalInterchangeFixture{
		entityType: entityType, entityID: entityID, documentID: uuid.New(),
		revision: revision, memberID: uuid.NewString(), source: source, plan: plan,
		base: base, sourceBody: sourceOverlay,
	}
}

func (f legalInterchangeFixture) raw(
	exists bool,
	withTargetBlock bool,
	title *string,
) legal.AIDocument {
	overlays := []*contentv1.RichTextLocaleOverlay{f.sourceBody}
	if withTargetBlock {
		overlays = append(overlays, &contentv1.RichTextLocaleOverlay{
			Locale: "ko",
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: f.sourceBody.Blocks[0].GetBlockId(),
				Value: &contentv1.RichTextBlockLocale_Paragraph{
					Paragraph: &contentv1.ParagraphBlockLocale{
						Props: &contentv1.ParagraphLocaleProps{},
						Content: []*contentv1.RichTextInline{{
							Value: &contentv1.RichTextInline_Text{
								Text: &contentv1.RichTextStyledText{Text: ""},
							},
						}},
					},
				},
			}},
		})
	}
	rows, err := contentv1.FlattenRichTextDocumentStorage(
		&contentv1.RichTextDocument{
			BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
			Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_POLICY,
			SourceLocale:            "en",
			Base:                    f.base,
			LocaleOverlays:          overlays,
		},
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	)
	if err != nil {
		panic(err)
	}
	var updatedAt *time.Time
	if exists {
		value := time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)
		updatedAt = &value
	}
	return legal.AIDocument{
		EntityType: f.entityType, EntityID: f.entityID, DocumentID: f.documentID,
		Revision: f.revision, SourceLocale: "en", Locale: "ko",
		LocaleExists: exists, LocaleUpdatedAt: updatedAt, Title: title,
		Rows: rows, ViewerMemberID: f.memberID,
	}
}

func (f legalInterchangeFixture) bodyHandle(t *testing.T) string {
	t.Helper()
	for _, unit := range f.plan.Units {
		if unit.ContainerType == core.ContainerTypeBlock {
			return unit.UnitID
		}
	}
	t.Fatal("Legal body unit not found")
	return ""
}
