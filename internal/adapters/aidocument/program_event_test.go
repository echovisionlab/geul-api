package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	programeventdomain "github.com/echovisionlab/geul-api/internal/programevent"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	programEventTestID   = "00000000-0000-4000-8000-000000000001"
	programEventBlockID  = "00000000-0000-4000-8000-000000000002"
	programEventRevision = "00000000-0000-4000-8000-000000000003"
)

type exactProgramEventDocumentAPI struct {
	state         programeventdomain.AIDocumentState
	result        programeventdomain.AIDocumentResult
	authorizeErr  error
	loadCalls     int
	executeCalls  int
	compilerCalls int
}

func (a *exactProgramEventDocumentAPI) LoadAIDocumentState(
	context.Context,
	string,
	string,
) (programeventdomain.AIDocumentState, error) {
	a.loadCalls++
	return a.state, nil
}

func (a *exactProgramEventDocumentAPI) ExecuteAIDocumentCommand(
	_ context.Context,
	eventID string,
	locale string,
	_ programeventdomain.AIDocumentExecutionMode,
	compiler programeventdomain.AIDocumentCommandCompiler,
) (programeventdomain.AIDocumentResult, error) {
	a.executeCalls++
	if a.authorizeErr != nil {
		return programeventdomain.AIDocumentResult{}, a.authorizeErr
	}
	if eventID != a.state.EventID || locale != a.state.RequestedLocale {
		return programeventdomain.AIDocumentResult{}, errors.New("unexpected Program Event identity or locale")
	}
	a.compilerCalls++
	if _, err := compiler(a.state); err != nil {
		return programeventdomain.AIDocumentResult{}, err
	}
	return a.result, nil
}

func TestNewProgramEventRegistrationRejectsMissingOwner(t *testing.T) {
	t.Parallel()
	if _, err := NewProgramEventRegistration(nil); err == nil {
		t.Fatal("missing Program Event owner was accepted")
	}
}

func TestProgramEventProjectionDistinguishesAbsentLocaleFromExplicitEmpty(t *testing.T) {
	t.Parallel()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	if err != nil {
		t.Fatal(err)
	}
	state := programeventdomain.AIDocumentState{
		EventID: programEventTestID, DocumentRevision: programEventRevision,
		SourceLocale: "en", RequestedLocale: "ko", LocaleExists: false,
		LocalizedDocument: programEventTestDocument("ko", false),
	}
	localized, err := programEventLocalizedDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := codec.Project(localized)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || len(nodes[0].Localized) != 0 {
		t.Fatalf("absent locale projected values: %+v", nodes)
	}

	state.LocaleExists = true
	state.LocalizedDocument.LocaleOverlay = &contentv1.RichTextLocaleOverlay{
		Locale: "ko",
		Blocks: []*contentv1.RichTextBlockLocale{{
			BlockId: programEventBlockID,
			Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
				Props: &contentv1.ParagraphLocaleProps{}, Content: []*contentv1.RichTextInline{},
			}},
		}},
	}
	localized, err = programEventLocalizedDocument(state)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err = codec.Project(localized)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes[0].Localized) != 1 || nodes[0].Localized[0].ID != richTextContentField ||
		nodes[0].Localized[0].Value.Kind != core.ValueKindInline || len(nodes[0].Localized[0].Value.Inline) != 0 {
		t.Fatalf("explicit empty locale value was not preserved: %+v", nodes[0].Localized)
	}
}

func TestProgramEventDomainValidationRequiresExplicitEmptyForLocaleValues(t *testing.T) {
	t.Parallel()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	if err != nil {
		t.Fatal(err)
	}
	document := core.Document{
		Identity:         core.DocumentIdentity{Domain: core.DomainProgramEvent, Reference: programEventTestID},
		DocumentRevision: programEventRevision, SourceLocale: "en", Locale: "ko", LocaleExists: true,
		Catalog: codec.Catalog(),
		Nodes: []core.Node{{
			ID: programEventBlockID, Kind: "paragraph",
			Localized: []core.FieldValue{{ID: richTextContentField, Value: core.RichText(core.InlineText("value"))}},
		}},
	}
	issues := validateProgramEventOperations(document, []core.Operation{
		core.UnsetFieldOperation(programEventBlockID, richTextContentField),
		core.SetFieldOperation(programEventBlockID, richTextContentField, core.RichText()),
	})
	if len(issues) != 1 || issues[0].Operation != 0 || issues[0].Code != core.IssueInvalidOperation {
		t.Fatalf("unexpected explicit-empty validation: %+v", issues)
	}
}

func TestValidateProgramEventIdentityRequiresExactDomainAndCanonicalUUID(t *testing.T) {
	t.Parallel()
	for _, identity := range []core.DocumentIdentity{
		{Domain: core.DomainPost, Reference: programEventTestID},
		{Domain: core.DomainProgramEvent, Reference: "not-a-uuid"},
		{Domain: core.DomainProgramEvent, Reference: "00000000-0000-4000-8000-000000000001 "},
	} {
		if err := validateProgramEventIdentity(identity); err == nil {
			t.Fatalf("invalid identity was accepted: %+v", identity)
		}
	}
	if err := validateProgramEventIdentity(core.DocumentIdentity{Domain: core.DomainProgramEvent, Reference: programEventTestID}); err != nil {
		t.Fatal(err)
	}
}

func TestProgramEventSemanticChangesExcludeNoOpFields(t *testing.T) {
	t.Parallel()
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	if err != nil {
		t.Fatal(err)
	}
	document := core.Document{
		Identity:         core.DocumentIdentity{Domain: core.DomainProgramEvent, Reference: programEventTestID},
		DocumentRevision: programEventRevision,
		SourceLocale:     "en", Locale: "en", LocaleExists: true,
		Catalog: codec.Catalog(),
		Nodes: []core.Node{{
			ID: programEventBlockID, Kind: "paragraph",
			Localized: []core.FieldValue{{ID: richTextContentField, Value: core.RichText(core.InlineText("before"))}},
		}},
	}
	changes, err := programEventSemanticChanges(document, []core.Operation{
		core.SetFieldOperation(programEventBlockID, richTextContentField, core.RichText(core.InlineText("before"))),
		core.SetFieldOperation(programEventBlockID, richTextContentField, core.RichText(core.InlineText("after"))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Operation != 1 || changes[0].Kind != core.OperationSetField {
		t.Fatalf("semantic changes = %+v", changes)
	}
}

func TestProgramEventExactMutationPathDoesNotEnterPublicLoad(t *testing.T) {
	registration, err := NewProgramEventRegistration(&programeventdomain.ProgramEventService{})
	require.NoError(t, err)
	port := registration.Port.(*programEventPort)
	eventID, documentID, revision, nextRevision, contributor :=
		uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	api := &exactProgramEventDocumentAPI{
		state: programeventdomain.AIDocumentState{
			EventID: eventID.String(), ContentDocumentID: documentID,
			DocumentRevision: revision.String(), SourceLocale: "en", RequestedLocale: "en", LocaleExists: true,
			LocalizedDocument: programEventTestDocument("en", true), ViewerMemberID: contributor.String(),
		},
		result: programeventdomain.AIDocumentResult{DocumentRevision: nextRevision.String(), Changed: true},
	}
	api.state.LocalizedDocument.Base.Nodes[0].Block.Id = programEventBlockID
	api.state.LocalizedDocument.LocaleOverlay.Blocks[0].BlockId = programEventBlockID
	port.service = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainProgramEvent,
		Document: core.DocumentReference(eventID.String()), Locale: "en",
		ExpectedDocumentRevision: core.Revision(revision.String()),
		Operations: []core.Operation{
			core.SetFieldOperation(programEventBlockID, richTextContentField, core.RichText(core.InlineText("changed"))),
		},
	}

	validation, err := service.Validate(context.Background(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid())
	result, err := service.Apply(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, core.Revision(nextRevision.String()), result.DocumentRevision)
	require.Equal(t, 2, api.executeCalls)
	require.Equal(t, 2, api.compilerCalls)
	require.Zero(t, api.loadCalls)
}

func TestProgramEventExactMutationAuthorizesBeforeCompiler(t *testing.T) {
	registration, err := NewProgramEventRegistration(&programeventdomain.ProgramEventService{})
	require.NoError(t, err)
	port := registration.Port.(*programEventPort)
	eventID := uuid.New()
	denied := errors.New("program event not found")
	api := &exactProgramEventDocumentAPI{
		state:        programeventdomain.AIDocumentState{EventID: eventID.String(), RequestedLocale: "en"},
		authorizeErr: denied,
	}
	port.service = api
	service, err := core.NewService(port)
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainProgramEvent,
		Document: core.DocumentReference(eventID.String()), Locale: "en",
		ExpectedDocumentRevision: core.Revision(uuid.NewString()),
		Operations:               []core.Operation{core.DeleteBlockOperation("unknown-block")},
	}

	_, err = service.Validate(context.Background(), request)
	require.ErrorIs(t, err, denied)
	require.Equal(t, 1, api.executeCalls)
	require.Zero(t, api.compilerCalls)
	require.Zero(t, api.loadCalls)
}

func programEventTestDocument(locale string, exists bool) *contentv1.LocalizedRichTextDocument {
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: contentv1.ContentBlockCatalogFingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT,
		Locale:                  locale,
		Base: &contentv1.RichTextBlockGraph{Nodes: []*contentv1.RichTextBlockNode{{
			Block: &contentv1.RichTextBlock{
				Id:    programEventBlockID,
				Value: &contentv1.RichTextBlock_Paragraph{Paragraph: &contentv1.ParagraphBlock{Props: &contentv1.ParagraphProps{}}},
			},
			Placement: &contentv1.ContentBlockPlacement{},
		}}},
		LocaleOverlay: &contentv1.RichTextLocaleOverlay{
			Locale: locale,
			Blocks: []*contentv1.RichTextBlockLocale{{
				BlockId: programEventBlockID,
				Value: &contentv1.RichTextBlockLocale_Paragraph{Paragraph: &contentv1.ParagraphBlockLocale{
					Props:   &contentv1.ParagraphLocaleProps{},
					Content: []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: &contentv1.RichTextStyledText{Text: "source"}}}},
				}},
			}},
		},
	}
	if !exists {
		document.LocaleOverlay.Blocks = nil
	}
	return document
}
