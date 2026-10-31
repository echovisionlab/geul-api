package aidocumentadapter

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	labeldomain "github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/model"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// The exact fakes these goldens reuse return their configured result right
// after the compiler; the wrappers below add the post-compile domain error
// (CAS conflict, persistence rejection) the scenarios need.

type goldenArtistDocumentAPI struct {
	*exactArtistDocumentAPI
	err error
}

func (a *goldenArtistDocumentAPI) ExecuteAIDocumentMutation(
	ctx context.Context,
	artistID, locale string,
	mode artistdomain.AIDocumentExecutionMode,
	compiler artistdomain.AIDocumentMutationCompiler,
) (artistdomain.AIDocumentMutationResult, error) {
	result, err := a.exactArtistDocumentAPI.ExecuteAIDocumentMutation(ctx, artistID, locale, mode, compiler)
	if err == nil && a.err != nil {
		return artistdomain.AIDocumentMutationResult{}, a.err
	}
	return result, err
}

type goldenReleaseDocumentAPI struct {
	*exactReleaseDocumentAPI
	err error
}

func (a *goldenReleaseDocumentAPI) ExecuteAIDocumentMutation(
	ctx context.Context,
	command releasedomain.ExecuteAIDocumentMutationCommand,
) (releasedomain.AIDocumentMutationResult, error) {
	result, err := a.exactReleaseDocumentAPI.ExecuteAIDocumentMutation(ctx, command)
	if err == nil && a.err != nil {
		return releasedomain.AIDocumentMutationResult{}, a.err
	}
	return result, err
}

type goldenWorkDocumentAPI struct {
	*exactWorkDocumentAPI
	err error
}

func (a *goldenWorkDocumentAPI) ExecuteAIDocumentMutation(
	ctx context.Context,
	workID, locale string,
	mode workdomain.AIDocumentExecutionMode,
	compiler workdomain.AIDocumentMutationCompiler,
) (workdomain.AIDocumentMutationResult, error) {
	result, err := a.exactWorkDocumentAPI.ExecuteAIDocumentMutation(ctx, workID, locale, mode, compiler)
	if err == nil && a.err != nil {
		return workdomain.AIDocumentMutationResult{}, a.err
	}
	return result, err
}

type goldenPostDocumentAPI struct {
	*exactPostDocumentAPI
	err error
}

func (a *goldenPostDocumentAPI) ExecuteAIDocumentMutation(
	ctx context.Context,
	postID, locale string,
	mode postdomain.AIDocumentExecutionMode,
	compiler postdomain.AIDocumentMutationCompiler,
) (postdomain.AIDocumentMutationResult, error) {
	result, err := a.exactPostDocumentAPI.ExecuteAIDocumentMutation(ctx, postID, locale, mode, compiler)
	if err == nil && a.err != nil {
		return postdomain.AIDocumentMutationResult{}, a.err
	}
	return result, err
}

// goldenParagraphDocument is a one-paragraph localized document whose locale
// overlay holds an explicit value, so source and target edits both compile.
func goldenParagraphDocument(profile contentv1.RichTextProfile, locale string) *contentv1.LocalizedRichTextDocument {
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 profile,
		Locale:                  locale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id:    goldenBlockID,
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}},
			},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{Locale: locale, Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: goldenBlockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props:   &contentv1.ParagraphLocaleProps{},
				Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: locale + " text"}}}},
			}},
		}}},
	}
}

const goldenFileBlockID = "0b000000-0000-4000-8000-000000000003"

// goldenFileDocument adds an empty generated File Block to a paragraph
// document so an attach operation reaches the owning domain.
func goldenFileDocument(t *testing.T, profile contentv1.RichTextProfile, locale string) *contentv1.LocalizedRichTextDocument {
	t.Helper()
	codec, err := NewRichTextCodec(profile)
	require.NoError(t, err)
	node, localeBlock, err := codec.newBlock("file", goldenFileBlockID)
	require.NoError(t, err)
	node.Placement = &contentv1.ContentBlockPlacement{}
	document := goldenParagraphDocument(profile, locale)
	document.Base.Nodes = append(document.Base.Nodes, node)
	document.LocaleOverlay.Blocks = append(document.LocaleOverlay.Blocks, localeBlock)
	return document
}

// goldenAttachFileRejection lets the owning domain reject a single File
// attachment, the case the File-aware adapters map to an indexed issue.
func goldenAttachFileRejection() mutationGoldenScenarioInput {
	return mutationGoldenScenarioInput{
		name:       "domain File reference error on attach",
		operations: []core.Operation{core.AttachFileOperation(goldenFileBlockID, "attachment", goldenFileID)},
		err:        errGoldenFileReference,
	}
}

func goldenContentEdit(text string) core.Operation {
	return core.SetFieldOperation(goldenBlockID, richTextContentField, core.RichText(core.InlineText(text)))
}

// goldenRichTextStructure inserts a paragraph, moves the existing one after
// it and deletes the inserted one again, covering the three graph operations.
func goldenRichTextStructure() []core.Operation {
	return []core.Operation{
		core.InsertBlockOperation(goldenSecondBlockID, "paragraph", "", goldenBlockID),
		core.MoveBlockOperation(goldenBlockID, "", goldenSecondBlockID),
		core.DeleteBlockOperation(goldenSecondBlockID),
	}
}

func goldenTargetRevisionFor(target bool) *string {
	if !target {
		return nil
	}
	return goldenStringPointer(goldenTargetRevision)
}

func goldenLocale(target bool) string {
	if target {
		return "ko"
	}
	return "en"
}

func TestArtistExactMutationGolden(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	require.NoError(t, err)
	api := &goldenArtistDocumentAPI{exactArtistDocumentAPI: &exactArtistDocumentAPI{}}
	service, err := core.NewService(&artistPort{service: api, codec: codec})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "artist", domain: core.DomainArtist, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: func(operation core.Operation) []string { return artistOperationHandles(operation, "ko") },
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			api.state = artistdomain.AIDocumentState{
				ArtistID: goldenEntityID, DocumentID: uuid.MustParse(goldenDocumentID), Revision: goldenDocumentRevision,
				TargetRevision: goldenTargetRevisionFor(target), SourceLocale: "en", Locale: locale, LocaleExists: true,
				Document:       goldenParagraphDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, locale),
				ViewerMemberID: goldenMemberID,
			}
			api.result = artistdomain.AIDocumentMutationResult{
				Revision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			api.err = err
		},
		conflict: func(target bool) error {
			if target {
				return &artistdomain.AIDocumentRevisionConflictError{
					Kind: artistdomain.AIDocumentTargetRevisionConflict, CurrentRevision: goldenDocumentRevision,
					CurrentTargetRevision: goldenStringPointer(goldenCurrentTargetRevision),
				}
			}
			return &artistdomain.AIDocumentRevisionConflictError{
				Kind: artistdomain.AIDocumentDocumentRevisionConflict, CurrentRevision: goldenCurrentDocumentRevision,
			}
		},
		sourceOps:    []core.Operation{goldenContentEdit("changed source")},
		targetOps:    []core.Operation{goldenContentEdit("changed target")},
		structureOps: goldenRichTextStructure(),
	})
}

func TestLabelExactMutationGolden(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	require.NoError(t, err)
	api := &labelRegistrationAPI{}
	service, err := core.NewService(&labelPort{api: api, codec: codec})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "label", domain: core.DomainLabel, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: func(operation core.Operation) []string { return labelOperationHandles(operation, "ko") },
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			api.state = labeldomain.AIDocumentState{
				LabelID: goldenEntityID, ContentDocumentID: uuid.MustParse(goldenDocumentID), Revision: goldenDocumentRevision,
				TargetRevision: goldenTargetRevisionFor(target), SourceLocale: "en", Locale: locale, LocaleExists: true,
				Document:       goldenParagraphDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, locale),
				ViewerMemberID: goldenMemberID,
			}
			api.result = labeldomain.AIDocumentMutationResult{
				Content:        contentblock.Result{DocumentRevision: uuid.MustParse(result.DocumentRevision), Changed: result.Changed},
				TargetRevision: result.TargetRevision,
			}
			api.validateErr, api.applyErr = err, err
		},
		conflict: func(target bool) error {
			if target {
				return &labeldomain.AIDocumentRevisionConflictError{
					Kind: labeldomain.AIDocumentTargetRevisionConflict, CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision: goldenStringPointer(goldenCurrentTargetRevision),
				}
			}
			return &labeldomain.AIDocumentRevisionConflictError{
				Kind: labeldomain.AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: goldenCurrentDocumentRevision,
			}
		},
		sourceOps:    []core.Operation{goldenContentEdit("changed source")},
		targetOps:    []core.Operation{goldenContentEdit("changed target")},
		structureOps: goldenRichTextStructure(),
	})
}

func TestReleaseExactMutationGolden(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT)
	require.NoError(t, err)
	api := &goldenReleaseDocumentAPI{exactReleaseDocumentAPI: &exactReleaseDocumentAPI{}}
	service, err := core.NewService(&releasePort{service: api, codec: codec, catalog: releaseCatalog(codec)})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "release", domain: core.DomainRelease, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: func(operation core.Operation) []string { return compactOperationHandles(operation, "ko", "release") },
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			title := locale + " title"
			api.state = releasedomain.AIDocumentState{
				ReleaseID: goldenEntityID, DocumentID: uuid.MustParse(goldenDocumentID), DocumentRevision: goldenDocumentRevision,
				TargetRevision: goldenTargetRevisionFor(target), SourceLocale: "en", Locale: locale, LocaleExists: true,
				Document:          goldenParagraphDocument(contentv1.RichTextProfile_RICH_TEXT_PROFILE_COMPACT, locale),
				RequestedMetadata: &releasedomain.AIDocumentLocaleMetadata{Title: &title},
				ViewerMemberID:    goldenMemberID,
			}
			api.result = releasedomain.AIDocumentMutationResult{
				DocumentRevision: result.DocumentRevision, TargetRevision: result.TargetRevision, Changed: result.Changed,
			}
			api.err = err
		},
		conflict: func(target bool) error {
			if target {
				return &releasedomain.AIDocumentRevisionConflictError{
					Kind: releasedomain.AIDocumentTargetRevisionConflict, CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision: goldenStringPointer(goldenCurrentTargetRevision),
				}
			}
			return &releasedomain.AIDocumentRevisionConflictError{
				Kind: releasedomain.AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: goldenCurrentDocumentRevision,
			}
		},
		sourceOps: []core.Operation{
			core.SetFieldOperation(releaseMetadataBlockID, releaseTitleField, core.Text("changed title")),
			goldenContentEdit("changed source"),
		},
		targetOps: []core.Operation{
			core.SetFieldOperation(releaseMetadataBlockID, releaseTitleField, core.Text("changed target title")),
			goldenContentEdit("changed target"),
		},
		structureOps: goldenRichTextStructure(),
	})
}

func TestWorkExactMutationGolden(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK)
	require.NoError(t, err)
	api := &goldenWorkDocumentAPI{exactWorkDocumentAPI: &exactWorkDocumentAPI{}}
	service, err := core.NewService(&workPort{application: api, codec: codec, catalog: workCatalog(codec)})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "work", domain: core.DomainWork, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: workOperationHandles,
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			title := locale + " title"
			revision := uuid.MustParse(goldenDocumentRevision)
			api.state = workdomain.AIDocumentState{
				Work: model.Work{ID: goldenEntityID}, SourceLocale: "en", Locale: locale, LocaleExists: true,
				DocumentRevision: goldenDocumentRevision, TargetRevision: goldenTargetRevisionFor(target), Title: &title,
				Snapshot: contentblock.Snapshot{Document: contentblock.Document{
					ID: uuid.MustParse(goldenDocumentID), Revision: revision,
				}},
				Document:       goldenFileDocument(t, contentv1.RichTextProfile_RICH_TEXT_PROFILE_WORK, locale),
				ViewerMemberID: goldenMemberID,
			}
			api.result = workdomain.AIDocumentMutationResult{
				Content:        contentblock.Result{DocumentRevision: uuid.MustParse(result.DocumentRevision), Changed: result.Changed},
				TargetRevision: result.TargetRevision,
			}
			api.err = err
		},
		conflict: func(target bool) error {
			if target {
				return &workdomain.AIDocumentRevisionConflictError{
					Kind: workdomain.AIDocumentTargetRevisionConflict, CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision: goldenStringPointer(goldenCurrentTargetRevision),
				}
			}
			return &workdomain.AIDocumentRevisionConflictError{
				Kind: workdomain.AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: goldenCurrentDocumentRevision,
			}
		},
		sourceOps: []core.Operation{
			core.SetFieldOperation(workMetadataBlockID, workTitleField, core.Text("changed title")),
			goldenContentEdit("changed source"),
		},
		targetOps: []core.Operation{
			core.SetFieldOperation(workMetadataBlockID, workTitleField, core.Text("changed target title")),
			goldenContentEdit("changed target"),
		},
		structureOps: goldenRichTextStructure(),
		extra:        []mutationGoldenScenarioInput{goldenAttachFileRejection()},
	})
}

func TestPostExactMutationGolden(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST)
	require.NoError(t, err)
	api := &goldenPostDocumentAPI{exactPostDocumentAPI: &exactPostDocumentAPI{}}
	service, err := core.NewService(&postPort{service: api, codec: codec, catalog: postCatalog(codec)})
	require.NoError(t, err)
	runMutationGolden(t, mutationGoldenSubject{
		name: "post", domain: core.DomainPost, reference: goldenEntityID,
		sourceLocale: "en", targetLocale: "ko", service: service,
		handles: postOperationHandles,
		arrange: func(target bool, result mutationGoldenDomainResult, err error) {
			locale := goldenLocale(target)
			title, sourceTitle := locale+" title", "en title"
			api.state = postdomain.AIDocumentState{
				PostID: goldenEntityID, ContentDocumentID: goldenDocumentID, DocumentRevision: goldenDocumentRevision,
				TargetRevision: goldenTargetRevisionFor(target), SourceLocale: "en", RequestedLocale: locale, LocaleExists: true,
				SourceMetadata:    postdomain.AIDocumentLocaleMetadata{Title: &sourceTitle},
				RequestedMetadata: &postdomain.AIDocumentLocaleMetadata{Title: &title},
				CategoryIDs:       []string{}, TagIDs: []string{},
				LocalizedDocument: goldenFileDocument(t, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, locale),
				ViewerMemberID:    goldenMemberID,
			}
			api.result = postdomain.AIDocumentMutationResult{
				Result:         contentblock.Result{DocumentRevision: uuid.MustParse(result.DocumentRevision), Changed: result.Changed},
				TargetRevision: result.TargetRevision,
			}
			api.err = err
		},
		conflict: func(target bool) error {
			if target {
				return &postdomain.AIDocumentRevisionConflictError{
					Kind: postdomain.AIDocumentTargetRevisionConflict, CurrentDocumentRevision: goldenDocumentRevision,
					CurrentTargetRevision: goldenStringPointer(goldenCurrentTargetRevision),
				}
			}
			return &postdomain.AIDocumentRevisionConflictError{
				Kind: postdomain.AIDocumentDocumentRevisionConflict, CurrentDocumentRevision: goldenCurrentDocumentRevision,
			}
		},
		sourceOps: []core.Operation{
			core.SetFieldOperation(postMetadataBlockID, postTitleField, core.Text("changed title")),
			goldenContentEdit("changed source"),
		},
		targetOps: []core.Operation{
			core.SetFieldOperation(postMetadataBlockID, postTitleField, core.Text("changed target title")),
			goldenContentEdit("changed target"),
		},
		structureOps: goldenRichTextStructure(),
		extra:        []mutationGoldenScenarioInput{goldenAttachFileRejection()},
	})
}
