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
	persistCalls  int
	command       programeventdomain.AIDocumentCommand
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
	command, err := compiler(a.state)
	if err != nil {
		return programeventdomain.AIDocumentResult{}, err
	}
	a.command = command
	a.persistCalls++
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

func TestProgramEventBatchRangeErrorPreservesTransportRejection(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	require.NoError(t, err)
	api := &exactProgramEventDocumentAPI{state: programeventdomain.AIDocumentState{
		EventID: programEventTestID, RequestedLocale: "en", SourceLocale: "en", LocaleExists: true,
		DocumentRevision: programEventRevision, ContentDocumentID: uuid.New(), ViewerMemberID: uuid.NewString(),
		LocalizedDocument: programEventTestDocument("en", true),
	}}
	port := &programEventPort{service: api, codec: codec}
	identity := core.DocumentIdentity{Domain: core.DomainProgramEvent, Reference: programEventTestID}
	document, err := port.Load(t.Context(), identity, "en")
	require.NoError(t, err)
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainProgramEvent, Document: identity.Reference,
		Locale: "en", ExpectedDocumentRevision: document.DocumentRevision,
		Operations: []core.Operation{core.SetFieldOperation(programEventBlockID, "previewWidth", core.Number("5"))},
	}
	_, generic := core.ValidateLoadedApply(document, request)
	require.True(t, generic.Valid(), "%+v", generic)
	application, err := core.NewService(port)
	require.NoError(t, err)
	assertBatchRangeErrorBoundaries(t, application, request)
	require.Zero(t, api.persistCalls, "rejected batch reached persistence")
}

func TestProgramEventMetadataProjectionUsesExactLocaleAndSourceTitle(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	require.NoError(t, err)
	port := &programEventPort{codec: codec}
	for _, test := range []struct {
		name    string
		exists  bool
		summary *string
	}{
		{name: "absent locale"}, {name: "unset summary", exists: true}, {name: "explicit empty", exists: true, summary: stringPointer("")},
	} {
		t.Run(test.name, func(t *testing.T) {
			var targetRevision *string
			if test.exists {
				targetRevision = stringPointer("target-revision")
			}
			document, err := port.project(core.DocumentIdentity{Domain: core.DomainProgramEvent, Reference: programEventTestID}, "ko", programeventdomain.AIDocumentState{
				EventID: programEventTestID, SourceLocale: "en", RequestedLocale: "ko", DocumentRevision: programEventRevision,
				TargetRevision: targetRevision, LocaleExists: test.exists, Title: "Source title", Summary: test.summary, LocalizedDocument: programEventTestDocument("ko", test.exists),
			})
			require.NoError(t, err)
			require.Equal(t, core.BlockID("document"), document.Nodes[0].ID)
			require.Equal(t, "Source title", document.Nodes[0].Shared[0].Value.Text)
			if test.summary == nil {
				require.Empty(t, document.Nodes[0].Localized)
			} else {
				require.Equal(t, "", document.Nodes[0].Localized[0].Value.Text)
			}
			title, ok := findCatalogField(document.Catalog, programEventMetadataBlockKind, programEventTitleField)
			require.True(t, ok)
			require.Equal(t, core.FieldOwnershipSource, title.Ownership)
			require.NotEqual(t, codec.Catalog().Fingerprint, document.Catalog.Fingerprint)
			_, err = core.EncodeOpenMetadata(core.OpenMetadata{Protocol: core.ProtocolVersion, Profile: document.Identity.Domain, Document: document.Identity.Reference, DocumentRevision: document.DocumentRevision, TargetRevision: document.TargetRevision, SourceLocale: document.SourceLocale, Locale: document.Locale, LocaleRole: document.Role(), LocaleExists: document.LocaleExists, Catalog: document.Catalog.Fingerprint})
			require.NoError(t, err)
		})
	}
}

func TestProgramEventMetadataExactMutationCompilesWithBodyAndPreservesIndexes(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	require.NoError(t, err)
	api := &exactProgramEventDocumentAPI{state: programeventdomain.AIDocumentState{
		EventID: programEventTestID, ContentDocumentID: uuid.New(), DocumentRevision: programEventRevision,
		SourceLocale: "en", RequestedLocale: "en", LocaleExists: true, ViewerMemberID: uuid.NewString(),
		Title: "Before", Summary: stringPointer("before summary"), LocalizedDocument: programEventTestDocument("en", true),
	}}
	service, err := core.NewService(&programEventPort{codec: codec, service: api})
	require.NoError(t, err)
	request := core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: core.DomainProgramEvent, Document: programEventTestID, Locale: "en", ExpectedDocumentRevision: programEventRevision,
		Operations: []core.Operation{core.SetFieldOperation("document", "title", core.Text("  New title  ")), core.UnsetFieldOperation("document", "summary"), core.SetFieldOperation(programEventBlockID, "content", core.RichText(core.InlineText("new body")))},
	}
	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid(), "%+v", validation)
	require.True(t, api.command.Metadata.SetTitle)
	require.Equal(t, "New title", *api.command.Metadata.Title)
	require.True(t, api.command.Metadata.SetSummary)
	require.Nil(t, api.command.Metadata.Summary)
	require.Len(t, api.command.Batch.Upserts, 1)
	require.Zero(t, api.loadCalls)
	validOperations := request.Operations
	for _, operation := range []core.Operation{core.SetFieldOperation("document", "title", core.Text(" ")), core.UnsetFieldOperation("document", "title")} {
		before := api.persistCalls
		request.Operations = []core.Operation{operation}
		validation, err := service.Validate(t.Context(), request)
		require.NoError(t, err)
		require.False(t, validation.Valid())
		require.Equal(t, before, api.persistCalls)
	}
	request.Operations = validOperations
	request.Operations[2] = core.SetFieldOperation(programEventBlockID, "previewWidth", core.Number("5"))
	_, err = service.Validate(t.Context(), request)
	require.Error(t, err, "generated range errors remain transport rejection after metadata removal")
}

func TestProgramEventMetadataTargetSummaryAndProtectedTitle(t *testing.T) {
	codec, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PROGRAM_EVENT)
	require.NoError(t, err)
	targetRevision := "target-revision"
	api := &exactProgramEventDocumentAPI{state: programeventdomain.AIDocumentState{
		EventID: programEventTestID, ContentDocumentID: uuid.New(), DocumentRevision: programEventRevision, TargetRevision: &targetRevision,
		SourceLocale: "en", RequestedLocale: "ko", LocaleExists: true, ViewerMemberID: uuid.NewString(), Title: "Source title", Summary: stringPointer("번역 요약"), LocalizedDocument: programEventTestDocument("ko", true),
	}}
	service, err := core.NewService(&programEventPort{codec: codec, service: api})
	require.NoError(t, err)
	request := core.ApplyRequest{Protocol: core.ProtocolVersion, Profile: core.DomainProgramEvent, Document: programEventTestID, Locale: "ko", ExpectedDocumentRevision: programEventRevision, ExpectedTargetRevision: (*core.Revision)(&targetRevision), Operations: []core.Operation{core.SetFieldOperation("document", "summary", core.Text(""))}}
	validation, err := service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.True(t, validation.Valid(), "%+v", validation)
	require.True(t, api.command.Metadata.SetSummary)
	require.Equal(t, "", *api.command.Metadata.Summary)
	require.False(t, api.command.Metadata.SetTitle)
	require.Empty(t, api.command.Batch.Upserts)
	require.Empty(t, api.command.Batch.LocaleGroups, "metadata-only mutation must not rewrite body overlays")
	for _, operation := range []core.Operation{core.SetFieldOperation("document", "title", core.Text("Target title")), core.DeleteBlockOperation("document"), core.UnsetFieldOperation("document", "summary")} {
		before := api.persistCalls
		request.Operations = []core.Operation{operation}
		validation, err := service.Validate(t.Context(), request)
		require.NoError(t, err)
		require.False(t, validation.Valid())
		require.Equal(t, before, api.persistCalls)
	}
	request.Operations = []core.Operation{core.SetFieldOperation("document", "summary", core.Text("new"))}
	request.ExpectedDocumentRevision = core.Revision(uuid.NewString())
	before := api.persistCalls
	validation, err = service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, validation.Conflict)
	require.Equal(t, before, api.persistCalls)
	request.ExpectedDocumentRevision = programEventRevision
	staleTarget := core.Revision("stale-target")
	request.ExpectedTargetRevision = &staleTarget
	validation, err = service.Validate(t.Context(), request)
	require.NoError(t, err)
	require.NotNil(t, validation.Conflict)
	require.Equal(t, core.ConflictTargetRevision, validation.Conflict.Code)
	require.Equal(t, before, api.persistCalls)
}
