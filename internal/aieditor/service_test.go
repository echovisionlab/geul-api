package aieditor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/llm"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var testOwner = principal{
	identity: auth.IdentityID("identity-1"),
	member:   auth.MemberID("member-1"),
	session:  auth.SessionID("session-1"),
}

type fakeDocumentService struct {
	openResult  aidocument.OpenMetadata
	openErr     error
	readResult  aidocument.Projection
	readErr     error
	applyResult aidocument.ApplyResult
	applyErr    error
	applyGate   chan struct{}
	applyStart  chan struct{}

	mu       sync.Mutex
	apply    []aidocument.ApplyRequest
	readCall []aidocument.ReadRequest
}

func (f *fakeDocumentService) Open(context.Context, aidocument.OpenRequest) (aidocument.OpenMetadata, error) {
	return f.openResult, f.openErr
}

func (f *fakeDocumentService) Read(_ context.Context, request aidocument.ReadRequest) (aidocument.Projection, error) {
	f.mu.Lock()
	f.readCall = append(f.readCall, request)
	f.mu.Unlock()
	return f.readResult, f.readErr
}

func (f *fakeDocumentService) Apply(ctx context.Context, request aidocument.ApplyRequest) (aidocument.ApplyResult, error) {
	f.mu.Lock()
	f.apply = append(f.apply, request)
	f.mu.Unlock()
	if f.applyStart != nil {
		select {
		case f.applyStart <- struct{}{}:
		default:
		}
	}
	if f.applyGate != nil {
		select {
		case <-ctx.Done():
			return aidocument.ApplyResult{}, ctx.Err()
		case <-f.applyGate:
		}
	}
	return f.applyResult, f.applyErr
}

func (f *fakeDocumentService) applyRequests() []aidocument.ApplyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]aidocument.ApplyRequest(nil), f.apply...)
}

type fakeProvider struct {
	session *fakeSession
	err     error

	mu   sync.Mutex
	spec []llm.SessionSpec
}

func (f *fakeProvider) StartSession(_ context.Context, spec llm.SessionSpec) (llm.Session, error) {
	f.mu.Lock()
	f.spec = append(f.spec, spec)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.session, nil
}

func (*fakeProvider) ProviderName() string { return "fake" }
func (*fakeProvider) ModelName() string    { return "fake-model" }

type fakeSession struct {
	responses []string
	errors    []error
	block     bool
	started   chan struct{}

	mu    sync.Mutex
	turns []llm.SessionTurn
}

func (f *fakeSession) GenerateText(ctx context.Context, turn llm.SessionTurn) (string, error) {
	f.mu.Lock()
	index := len(f.turns)
	f.turns = append(f.turns, turn)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if index < len(f.errors) && f.errors[index] != nil {
		return "", f.errors[index]
	}
	if index >= len(f.responses) {
		return "", errors.New("unexpected provider turn")
	}
	return f.responses[index], nil
}

func (*fakeSession) Close(context.Context) error { return nil }

func TestApprovedToolCallAppliesServerConstructedMutationExactlyOnce(t *testing.T) {
	document := newFakeDocument()
	document.applyStart = make(chan struct{}, 1)
	document.applyGate = make(chan struct{})
	operations := []aidocument.Operation{aidocument.SetFieldOperation("block-a", "content", aidocument.RichText(aidocument.InlineText("revised")))}
	provider := &fakeProvider{session: &fakeSession{responses: []string{
		providerJSON(t, "I can revise this.", operations, "Revise paragraph", false),
		providerJSON(t, "The paragraph was revised.", nil, "", true),
	}}}
	service, err := NewService(document, provider)
	require.NoError(t, err)

	events, eventSignal, done := startTurn(t, service, testOwner, newStartRequest())
	approval := waitForApproval(t, events, eventSignal)
	require.Equal(t, "post-1", approval.Mutation.Document.Reference)
	require.Equal(t, "en", approval.Mutation.Locale.Code)
	require.Equal(t, "revision-1", approval.Mutation.ExpectedDocumentRevision)
	require.Equal(t, "target-revision-1", approval.Mutation.GetExpectedTargetRevision())

	err = service.resolve(testOwner, &managev1.ResolveAIEditorToolCallRequest{
		TurnId: events.turnID(), ToolCallId: approval.ToolCallId,
		Decision: managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_APPROVE,
	})
	require.NoError(t, err)
	select {
	case <-document.applyStart:
	case <-time.After(time.Second):
		t.Fatal("document apply did not start")
	}
	err = service.resolve(testOwner, &managev1.ResolveAIEditorToolCallRequest{
		TurnId: events.turnID(), ToolCallId: approval.ToolCallId,
		Decision: managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_APPROVE,
	})
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	close(document.applyGate)
	require.NoError(t, <-done)

	requests := document.applyRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, aidocument.DomainPost, requests[0].Profile)
	assert.Equal(t, aidocument.DocumentReference("post-1"), requests[0].Document)
	assert.Equal(t, aidocument.Locale("en"), requests[0].Locale)
	assert.Equal(t, aidocument.Revision("revision-1"), requests[0].ExpectedDocumentRevision)
	require.NotNil(t, requests[0].ExpectedTargetRevision)
	assert.Equal(t, aidocument.Revision("target-revision-1"), *requests[0].ExpectedTargetRevision)
	require.Len(t, requests[0].Operations, 1)
	assert.Equal(t, aidocument.OperationSetField, requests[0].Operations[0].Kind)

	allEvents := events.snapshot()
	require.NotNil(t, findAcceptedResult(allEvents))
	terminal := findTerminal(allEvents)
	require.NotNil(t, terminal)
	assert.Equal(t, managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_COMPLETED, terminal.Status)
	assert.Equal(t, []string{
		"phase:provider", "assistant", "phase:approval", "approval", "phase:applying",
		"document-result", "phase:provider", "assistant", "terminal:completed",
	}, eventKinds(allEvents))
	require.Len(t, provider.spec, 1)
	assert.Contains(t, provider.spec[0].SystemPrompt, "Never emit HTML")
	assert.NotNil(t, provider.spec[0].ResponseJSONSchema)
}

func TestToolDecisionIsBoundToExactBrowserSession(t *testing.T) {
	document := newFakeDocument()
	operations := []aidocument.Operation{aidocument.UnsetFieldOperation("block-a", "content")}
	provider := &fakeProvider{session: &fakeSession{responses: []string{
		providerJSON(t, "", operations, "Clear paragraph", false),
		providerJSON(t, "No changes were made.", nil, "", true),
	}}}
	service, err := NewService(document, provider)
	require.NoError(t, err)
	events, signal, done := startTurn(t, service, testOwner, newStartRequest())
	approval := waitForApproval(t, events, signal)

	otherSession := testOwner
	otherSession.session = "session-2"
	err = service.resolve(otherSession, &managev1.ResolveAIEditorToolCallRequest{
		TurnId: events.turnID(), ToolCallId: approval.ToolCallId,
		Decision: managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_DENY,
	})
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	err = service.resolve(testOwner, &managev1.ResolveAIEditorToolCallRequest{
		TurnId: events.turnID(), ToolCallId: approval.ToolCallId,
		Decision: managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_DENY,
	})
	require.NoError(t, err)
	require.NoError(t, <-done)
	assert.Empty(t, document.applyRequests())
}

func TestCancelIsBoundToSessionAndTerminatesProviderTurn(t *testing.T) {
	document := newFakeDocument()
	providerSession := &fakeSession{block: true, started: make(chan struct{}, 1)}
	service, err := NewService(document, &fakeProvider{session: providerSession})
	require.NoError(t, err)
	events, _, done := startTurn(t, service, testOwner, newStartRequest())
	select {
	case <-providerSession.started:
	case <-time.After(time.Second):
		t.Fatal("provider call did not start")
	}

	otherMember := testOwner
	otherMember.member = "member-2"
	_, err = service.CancelAIEditorTurn(authContext(otherMember), connect.NewRequest(&managev1.CancelAIEditorTurnRequest{TurnId: events.turnID()}))
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	_, err = service.CancelAIEditorTurn(authContext(testOwner), connect.NewRequest(&managev1.CancelAIEditorTurnRequest{TurnId: events.turnID()}))
	require.NoError(t, err)
	require.NoError(t, <-done)
	terminal := findTerminal(events.snapshot())
	require.NotNil(t, terminal)
	assert.Equal(t, managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, terminal.Status)
	assert.Empty(t, document.applyRequests())
}

func TestCancelDuringDocumentApplyTerminatesAsCancelled(t *testing.T) {
	document := newFakeDocument()
	document.applyStart = make(chan struct{}, 1)
	document.applyGate = make(chan struct{})
	operations := []aidocument.Operation{aidocument.UnsetFieldOperation("block-a", "content")}
	service, err := NewService(document, &fakeProvider{session: &fakeSession{responses: []string{
		providerJSON(t, "", operations, "Clear paragraph", false),
	}}})
	require.NoError(t, err)
	events, signal, done := startTurn(t, service, testOwner, newStartRequest())
	approval := waitForApproval(t, events, signal)
	require.NoError(t, service.resolve(testOwner, &managev1.ResolveAIEditorToolCallRequest{
		TurnId: events.turnID(), ToolCallId: approval.ToolCallId,
		Decision: managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_APPROVE,
	}))
	select {
	case <-document.applyStart:
	case <-time.After(time.Second):
		t.Fatal("document apply did not start")
	}
	_, err = service.CancelAIEditorTurn(authContext(testOwner), connect.NewRequest(&managev1.CancelAIEditorTurnRequest{TurnId: events.turnID()}))
	require.NoError(t, err)
	require.NoError(t, <-done)
	require.Len(t, document.applyRequests(), 1)
	terminal := findTerminal(events.snapshot())
	require.NotNil(t, terminal)
	assert.Equal(t, managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, terminal.Status)
}

func TestProviderAndDocumentFailuresUseBoundedTerminalCodes(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		document := newFakeDocument()
		provider := &fakeProvider{session: &fakeSession{errors: []error{errors.New("secret provider body")}}}
		service, err := NewService(document, provider)
		require.NoError(t, err)
		events, _, done := startTurn(t, service, testOwner, newStartRequest())
		require.NoError(t, <-done)
		terminal := findTerminal(events.snapshot())
		require.NotNil(t, terminal)
		assert.Equal(t, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_PROVIDER, terminal.GetFailureCode())
	})

	t.Run("document", func(t *testing.T) {
		document := newFakeDocument()
		document.openErr = errors.New("private document error")
		service, err := NewService(document, &fakeProvider{session: &fakeSession{}})
		require.NoError(t, err)
		events, _, done := startTurn(t, service, testOwner, newStartRequest())
		require.NoError(t, <-done)
		terminal := findTerminal(events.snapshot())
		require.NotNil(t, terminal)
		assert.Equal(t, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_DOCUMENT, terminal.GetFailureCode())
	})
}

func TestRejectedDocumentMutationIsReportedWithoutRetryingApply(t *testing.T) {
	document := newFakeDocument()
	document.applyErr = &aidocument.ValidationError{Result: aidocument.ValidationResult{Issues: []aidocument.OperationIssue{{
		Operation: 0, Code: aidocument.IssueUnknownField, Handle: "content", Message: "not exposed",
	}}}}
	operations := []aidocument.Operation{aidocument.UnsetFieldOperation("block-a", "content")}
	provider := &fakeProvider{session: &fakeSession{responses: []string{
		providerJSON(t, "", operations, "Clear paragraph", false),
		providerJSON(t, "The document rejected that change.", nil, "", true),
	}}}
	service, err := NewService(document, provider)
	require.NoError(t, err)
	events, signal, done := startTurn(t, service, testOwner, newStartRequest())
	approval := waitForApproval(t, events, signal)
	require.NoError(t, service.resolve(testOwner, &managev1.ResolveAIEditorToolCallRequest{
		TurnId: events.turnID(), ToolCallId: approval.ToolCallId,
		Decision: managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_APPROVE,
	}))
	require.NoError(t, <-done)
	require.Len(t, document.applyRequests(), 1)
	rejected := findRejectedResult(events.snapshot())
	require.NotNil(t, rejected)
	require.Len(t, rejected.Issues, 1)
	assert.Equal(t, managev1.AIDocumentIssueCode_AI_DOCUMENT_ISSUE_CODE_UNKNOWN_FIELD, rejected.Issues[0].Code)
}

func TestBrowserPrincipalRejectsMissingOrIncompleteSession(t *testing.T) {
	_, err := browserPrincipal(context.Background())
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{Authenticated: true, Onboarded: true, MemberID: "member"})
	_, err = browserPrincipal(ctx)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	ctx = auth.WithUser(context.Background(), &auth.UserInfo{Authenticated: true, Onboarded: true, Banned: true, IdentityID: "identity", MemberID: "member", SessionID: "session"})
	_, err = browserPrincipal(ctx)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}

func TestAcceptedProviderPromptCarriesAuthoritativeSplitRevision(t *testing.T) {
	target := aidocument.Revision("target-revision-2")
	withTarget := acceptedProviderPrompt(aidocument.ApplyResult{
		DocumentRevision: "document-revision-1", TargetRevision: &target,
	})
	assert.Contains(t, withTarget, `document revision is "document-revision-1"`)
	assert.Contains(t, withTarget, `target revision is "target-revision-2"`)

	withoutTarget := acceptedProviderPrompt(aidocument.ApplyResult{DocumentRevision: "document-revision-2"})
	assert.Contains(t, withoutTarget, `document revision is "document-revision-2"`)
	assert.Contains(t, withoutTarget, "there is no target revision")
}

func newFakeDocument() *fakeDocumentService {
	targetRevision := aidocument.Revision("target-revision-1")
	nextTargetRevision := aidocument.Revision("target-revision-2")
	return &fakeDocumentService{
		openResult: aidocument.OpenMetadata{
			Protocol: aidocument.ProtocolVersion, Profile: aidocument.DomainPost, Catalog: "catalog-1",
			Document: "post-1", DocumentRevision: "revision-1", TargetRevision: &targetRevision,
			SourceLocale: "ko", Locale: "en", LocaleRole: aidocument.LocaleRoleNonSource, LocaleExists: true,
		},
		readResult: aidocument.Projection{
			Protocol: aidocument.ProtocolVersion, Profile: aidocument.DomainPost, Catalog: "catalog-1",
			Document: "post-1", DocumentRevision: "revision-1", TargetRevision: &targetRevision,
			SourceLocale: "ko", Locale: "en", LocaleRole: aidocument.LocaleRoleNonSource, LocaleExists: true, Mode: aidocument.ReadBlocks,
			Nodes: []aidocument.Node{{ID: "block-a", Kind: "paragraph", Localized: []aidocument.FieldValue{{ID: "content", Value: aidocument.RichText(aidocument.InlineText("original"))}}}},
		},
		applyResult: aidocument.ApplyResult{
			DocumentRevision: "revision-1", TargetRevision: &nextTargetRevision, Changed: true,
			Changes: []aidocument.Change{{Operation: 0, Kind: aidocument.OperationSetField, AffectedHandles: []string{"block-a", "content"}}},
		},
	}
}

func newStartRequest() *managev1.StartAIEditorTurnRequest {
	prompt := "Make this clearer"
	targetRevision := "target-revision-1"
	return &managev1.StartAIEditorTurnRequest{
		Document: &managev1.AIDocumentReference{Domain: managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST, Reference: "post-1"},
		Locale:   &managev1.AIDocumentLocale{Code: "en"}, ExpectedDocumentRevision: "revision-1", ExpectedTargetRevision: &targetRevision,
		BlockHandles: []string{"block-a"}, Action: "improve-writing", Prompt: &prompt,
	}
}

func providerJSON(t *testing.T, text string, operations []aidocument.Operation, summary string, complete bool) string {
	t.Helper()
	if operations == nil {
		operations = []aidocument.Operation{}
	}
	encoded, err := json.Marshal(providerResponse{AssistantText: text, Operations: operations, Summary: summary, Complete: complete})
	require.NoError(t, err)
	return string(encoded)
}

type eventRecorder struct {
	mu     sync.Mutex
	events []*managev1.AIEditorTurnEvent
}

func (r *eventRecorder) add(event *managev1.AIEditorTurnEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) snapshot() []*managev1.AIEditorTurnEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*managev1.AIEditorTurnEvent(nil), r.events...)
}

func (r *eventRecorder) turnID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return ""
	}
	return r.events[0].TurnId
}

func startTurn(t *testing.T, service *Service, owner principal, request *managev1.StartAIEditorTurnRequest) (*eventRecorder, <-chan struct{}, <-chan error) {
	t.Helper()
	recorder := &eventRecorder{}
	signal := make(chan struct{}, 32)
	done := make(chan error, 1)
	go func() {
		done <- service.start(context.Background(), owner, request, func(event *managev1.AIEditorTurnEvent) error {
			recorder.add(event)
			signal <- struct{}{}
			return nil
		})
	}()
	return recorder, signal, done
}

func waitForApproval(t *testing.T, events *eventRecorder, signal <-chan struct{}) *managev1.AIEditorDocumentToolApprovalRequired {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		for _, event := range events.snapshot() {
			if event.GetApprovalRequired() != nil {
				return event.GetApprovalRequired()
			}
		}
		select {
		case <-signal:
		case <-timer.C:
			t.Fatal("approval event was not emitted")
		}
	}
}

func findTerminal(events []*managev1.AIEditorTurnEvent) *managev1.AIEditorTurnTerminalOutcome {
	for _, event := range events {
		if event.GetTerminal() != nil {
			return event.GetTerminal()
		}
	}
	return nil
}

func findAcceptedResult(events []*managev1.AIEditorTurnEvent) *managev1.AIDocumentAcceptedMutation {
	for _, event := range events {
		if result := event.GetDocumentResult(); result != nil {
			return result.GetAccepted()
		}
	}
	return nil
}

func findRejectedResult(events []*managev1.AIEditorTurnEvent) *managev1.AIDocumentValidation {
	for _, event := range events {
		if result := event.GetDocumentResult(); result != nil {
			return result.GetRejected()
		}
	}
	return nil
}

func eventKinds(events []*managev1.AIEditorTurnEvent) []string {
	result := make([]string, 0, len(events))
	for _, event := range events {
		switch {
		case event.GetAssistantText() != nil:
			result = append(result, "assistant")
		case event.GetApprovalRequired() != nil:
			result = append(result, "approval")
		case event.GetDocumentResult() != nil:
			result = append(result, "document-result")
		case event.GetPhase() != nil:
			result = append(result, map[managev1.AIEditorTurnPhase]string{
				managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_PROVIDER_RUNNING:       "phase:provider",
				managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_AWAITING_TOOL_APPROVAL: "phase:approval",
				managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_APPLYING_DOCUMENT:      "phase:applying",
			}[event.GetPhase().Phase])
		case event.GetTerminal() != nil:
			result = append(result, map[managev1.AIEditorTurnTerminalStatus]string{
				managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_COMPLETED: "terminal:completed",
				managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED: "terminal:cancelled",
				managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_FAILED:    "terminal:failed",
			}[event.GetTerminal().Status])
		}
	}
	return result
}

func authContext(owner principal) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: owner.identity, MemberID: owner.member, SessionID: owner.session,
		Authenticated: true, Onboarded: true,
	})
}
