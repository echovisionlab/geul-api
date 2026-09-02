// Package aieditor implements the transient first-party editor AI tool loop.
// It is authenticated exclusively by the browser session context and does not
// share authentication or transport state with Remote MCP.
package aieditor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

const (
	maxActiveTurns      = 64
	maxPromptBytes      = 16 << 10
	maxProviderResponse = 1 << 20
	maxContextBlocks    = 256
	maxToolCalls        = 4
	turnLifetime        = 5 * time.Minute
	providerCallTimeout = 45 * time.Second
	closeTimeout        = 2 * time.Second
)

var providerResponseSchema = structured.Fields{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"assistant_text", "operations", "summary", "complete"},
	"properties": structured.Fields{
		"assistant_text": structured.Fields{"type": "string"},
		"operations": structured.Fields{
			"type":        "array",
			"description": "A JSON array of compact dcdp/1 typed operations. Never include document identity, locale, or revision.",
			"items":       structured.Fields{"type": "array"},
		},
		"summary":  structured.Fields{"type": "string"},
		"complete": structured.Fields{"type": "boolean"},
	},
}

const providerSystemPrompt = `You are the first-party Geul document editor assistant.
The document context is canonical dcdp/1 data. Never emit HTML, Tiptap, ProseMirror, Yjs, numeric positions, document identity, locale, or revision.
Respond only with the required JSON object. "operations" is a compact dcdp/1 operation array using stable handles from the supplied context, or [] when no mutation is proposed. A mutation requires user approval, so do not claim it was applied before receiving a tool result. Set complete=true only when operations is empty. Keep summary short and non-sensitive.`

const providerOperationGrammar = `Compact operation grammar:
fs=["fs",target,value]; fu=["fu",target]; bi=["bi",block,kind,parentOrEmpty,afterOrEmpty]; bd=["bd",block]; bm=["bm",block,parentOrEmpty,afterOrEmpty]; bk=["bk",block,kind];
ri=["ri",block,relation,item,itemKind,afterOrEmpty]; rd=["rd",block,relation,item]; rm=["rm",block,relation,item,destinationBlock,destinationRelation,afterOrEmpty];
fa=["fa",target,fileHandle]; fd=["fd",target]; lc=["lc"]; ld=["ld"].
target=[block,"","",field] for a block field, or [block,relation,item,field] for a relation item field; an optional fifth item is a typed nested path of ["f",field] or ["i",stableItem].
value=["t",text], ["b",boolean], ["n",canonicalDecimalString], ["i",inlineItems], ["l",listItems], or ["o",objectFields].
listItem=[stableItemOrEmpty,value]; objectField=[field,value]. Inline items: ["t",text], ["b",children], ["em",children], ["u",children], ["s",children], ["code",children], ["fg",color,children], ["bg",color,children], ["a",url,children], ["br"], ["math",expression], ["ph",stableHandle].`

// DocumentService is the application-facing subset of aidocument.Service.
// Domain authorization and revision CAS remain owned by that service's port.
type DocumentService interface {
	Open(context.Context, aidocument.OpenRequest) (aidocument.OpenMetadata, error)
	Read(context.Context, aidocument.ReadRequest) (aidocument.Projection, error)
	Apply(context.Context, aidocument.ApplyRequest) (aidocument.ApplyResult, error)
}

type Provider interface {
	StartSession(context.Context, llm.SessionSpec) (llm.Session, error)
	ProviderName() string
	ModelName() string
}

type principal struct {
	identity auth.IdentityID
	member   auth.MemberID
	session  auth.SessionID
}

func (p principal) equal(other principal) bool {
	return p.identity == other.identity && p.member == other.member && p.session == other.session
}

type toolResolution struct {
	decision managev1.AIEditorToolCallDecision
}

type pendingToolCall struct {
	id       string
	resolved bool
	result   chan toolResolution
}

type activeTurn struct {
	owner     principal
	cancel    context.CancelFunc
	pending   *pendingToolCall
	cancelled bool
}

// Service implements the generated first-party Browser Session RPC surface.
// All turn state is process-local and removed when the stream terminates.
type Service struct {
	managev1connect.UnimplementedAIEditorOrchestrationServiceHandler

	document DocumentService
	provider Provider

	mu    sync.Mutex
	turns map[string]*activeTurn
}

func NewService(document DocumentService, provider Provider) (*Service, error) {
	if document == nil {
		return nil, errors.New("AI document service is required")
	}
	if provider == nil {
		return nil, errors.New("AI provider is required")
	}
	return &Service{document: document, provider: provider, turns: make(map[string]*activeTurn)}, nil
}

func (s *Service) StartAIEditorTurn(
	ctx context.Context,
	request *connect.Request[managev1.StartAIEditorTurnRequest],
	stream *connect.ServerStream[managev1.AIEditorTurnEvent],
) error {
	owner, err := browserPrincipal(ctx)
	if err != nil {
		return err
	}
	if request == nil || request.Msg == nil {
		return errs.InvalidArgumentMsg("request is required")
	}
	return s.start(ctx, owner, request.Msg, stream.Send)
}

func (s *Service) ResolveAIEditorToolCall(
	ctx context.Context,
	request *connect.Request[managev1.ResolveAIEditorToolCallRequest],
) (*connect.Response[managev1.ResolveAIEditorToolCallResponse], error) {
	owner, err := browserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil {
		return nil, errs.InvalidArgumentMsg("request is required")
	}
	if err := s.resolve(owner, request.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.ResolveAIEditorToolCallResponse{}), nil
}

func (s *Service) CancelAIEditorTurn(
	ctx context.Context,
	request *connect.Request[managev1.CancelAIEditorTurnRequest],
) (*connect.Response[managev1.CancelAIEditorTurnResponse], error) {
	owner, err := browserPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if request == nil || request.Msg == nil || request.Msg.TurnId == "" {
		return nil, errs.InvalidArgument("turn_id", "must be provided")
	}

	s.mu.Lock()
	turn, exists := s.turns[request.Msg.TurnId]
	if !exists {
		s.mu.Unlock()
		return nil, errs.NotFoundMsg("AI editor turn not found")
	}
	if !turn.owner.equal(owner) {
		s.mu.Unlock()
		return nil, errs.PermissionDenied("AI editor turn belongs to another browser session")
	}
	if turn.cancelled {
		s.mu.Unlock()
		return nil, errs.FailedPrecondition("AI editor turn is already cancelled")
	}
	turn.cancelled = true
	cancel := turn.cancel
	s.mu.Unlock()
	cancel()
	return connect.NewResponse(&managev1.CancelAIEditorTurnResponse{}), nil
}

type emitFunc func(*managev1.AIEditorTurnEvent) error

func (s *Service) start(
	ctx context.Context,
	owner principal,
	request *managev1.StartAIEditorTurnRequest,
	emit emitFunc,
) error {
	documentID, locale, err := validateStartRequest(request)
	if err != nil {
		return err
	}

	turnID := uuid.NewString()
	turnCtx, cancel := context.WithTimeout(ctx, turnLifetime)
	turn := &activeTurn{owner: owner, cancel: cancel}
	if err := s.reserveTurn(turnID, turn); err != nil {
		cancel()
		return err
	}
	defer func() {
		cancel()
		s.removeTurn(turnID, turn)
	}()

	send := func(event *managev1.AIEditorTurnEvent) error {
		event.TurnId = turnID
		return emit(event)
	}
	phase := func(value managev1.AIEditorTurnPhase) error {
		return send(&managev1.AIEditorTurnEvent{Event: &managev1.AIEditorTurnEvent_Phase{Phase: &managev1.AIEditorTurnPhaseUpdate{Phase: value}}})
	}
	terminal := func(status managev1.AIEditorTurnTerminalStatus, failure managev1.AIEditorTurnFailureCode) error {
		outcome := &managev1.AIEditorTurnTerminalOutcome{Status: status}
		if failure != managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED {
			outcome.FailureCode = &failure
		}
		return send(&managev1.AIEditorTurnEvent{Event: &managev1.AIEditorTurnEvent_Terminal{Terminal: outcome}})
	}

	metadata, err := s.document.Open(turnCtx, aidocument.OpenRequest{Document: documentID, Locale: locale})
	if err != nil {
		return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_DOCUMENT)
	}
	expectedTargetRevision := optionalRevision(request.ExpectedTargetRevision)
	if metadata.DocumentRevision != aidocument.Revision(request.ExpectedDocumentRevision) ||
		!sameRevision(metadata.TargetRevision, expectedTargetRevision) {
		return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_DOCUMENT)
	}
	projection, err := s.readContext(turnCtx, documentID, locale, request.BlockHandles)
	if err != nil || projection.DocumentRevision != metadata.DocumentRevision ||
		!sameRevision(projection.TargetRevision, metadata.TargetRevision) {
		return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_DOCUMENT)
	}
	encodedContext, err := aidocument.EncodeProjection(projection)
	if err != nil {
		return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_INTERNAL)
	}

	if err := phase(managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_PROVIDER_RUNNING); err != nil {
		return err
	}
	providerSession, err := s.provider.StartSession(turnCtx, llm.SessionSpec{
		RequestID:          turnID,
		Action:             request.Action,
		SystemPrompt:       providerSystemPrompt,
		ResponseJSONSchema: providerResponseSchema,
		ResponseSchemaName: "geul_ai_editor_turn",
		Timeout:            providerCallTimeout,
	})
	if err != nil {
		return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_PROVIDER)
	}
	defer closeProviderSession(providerSession)

	providerPrompt := initialProviderPrompt(request, encodedContext)
	for toolIndex := 0; toolIndex <= maxToolCalls; toolIndex++ {
		responseText, callErr := providerSession.GenerateText(turnCtx, llm.SessionTurn{
			OperationID: fmt.Sprintf("%s:%d", turnID, toolIndex),
			TurnKind:    providerTurnKind(toolIndex),
			UserPrompt:  providerPrompt,
		})
		if callErr != nil {
			if errors.Is(turnCtx.Err(), context.Canceled) || errors.Is(turnCtx.Err(), context.DeadlineExceeded) {
				return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED)
			}
			return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_PROVIDER)
		}
		response, parseErr := parseProviderResponse(
			responseText, documentID, locale, metadata.DocumentRevision, metadata.TargetRevision,
		)
		if parseErr != nil {
			return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_PROVIDER)
		}
		if response.AssistantText != "" {
			if err := send(&managev1.AIEditorTurnEvent{Event: &managev1.AIEditorTurnEvent_AssistantText{AssistantText: &managev1.AIEditorAssistantTextDelta{Text: response.AssistantText}}}); err != nil {
				return err
			}
		}
		if response.Complete {
			return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_COMPLETED, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED)
		}
		if toolIndex == maxToolCalls {
			return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_PROVIDER)
		}

		toolCallID := uuid.NewString()
		pending := &pendingToolCall{id: toolCallID, result: make(chan toolResolution, 1)}
		if !s.setPending(turnID, turn, pending) {
			return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED)
		}
		if err := phase(managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_AWAITING_TOOL_APPROVAL); err != nil {
			return err
		}
		if err := send(&managev1.AIEditorTurnEvent{Event: &managev1.AIEditorTurnEvent_ApprovalRequired{ApprovalRequired: &managev1.AIEditorDocumentToolApprovalRequired{
			ToolCallId: toolCallID,
			Mutation:   mutationToProto(response.Mutation),
			Summary:    optionalString(response.Summary),
		}}}); err != nil {
			return err
		}

		var resolution toolResolution
		select {
		case <-turnCtx.Done():
			return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED)
		case resolution = <-pending.result:
		}
		s.clearPending(turnID, turn, pending)
		if resolution.decision == managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_DENY {
			providerPrompt = `The user denied the proposed document mutation. Do not apply or repeat it. Respond with a completed assistant message or a materially different mutation.`
			if err := phase(managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_PROVIDER_RUNNING); err != nil {
				return err
			}
			continue
		}

		if turnCtx.Err() != nil {
			return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED)
		}
		if err := phase(managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_APPLYING_DOCUMENT); err != nil {
			return err
		}
		result, applyErr := s.document.Apply(turnCtx, response.Mutation)
		if applyErr != nil {
			if turnCtx.Err() != nil {
				return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_CANCELLED, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_UNSPECIFIED)
			}
			rejected, ok := rejectedMutationToProto(applyErr)
			if !ok {
				return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_DOCUMENT)
			}
			if err := send(&managev1.AIEditorTurnEvent{Event: &managev1.AIEditorTurnEvent_DocumentResult{DocumentResult: &managev1.AIEditorDocumentToolResult{
				ToolCallId: toolCallID,
				Result:     &managev1.AIEditorDocumentToolResult_Rejected{Rejected: rejected},
			}}}); err != nil {
				return err
			}
			providerPrompt = `The document rejected the proposed mutation because validation or revision CAS failed. Do not claim it was applied. Finish with a concise explanation.`
		} else {
			accepted := acceptedMutationToProto(result)
			if err := send(&managev1.AIEditorTurnEvent{Event: &managev1.AIEditorTurnEvent_DocumentResult{DocumentResult: &managev1.AIEditorDocumentToolResult{
				ToolCallId: toolCallID,
				Result:     &managev1.AIEditorDocumentToolResult_Accepted{Accepted: accepted},
			}}}); err != nil {
				return err
			}
			providerPrompt = acceptedProviderPrompt(result)
		}
		if err := phase(managev1.AIEditorTurnPhase_AI_EDITOR_TURN_PHASE_PROVIDER_RUNNING); err != nil {
			return err
		}
	}
	return emitTerminalFailure(terminal, managev1.AIEditorTurnFailureCode_AI_EDITOR_TURN_FAILURE_CODE_INTERNAL)
}

type providerResponse struct {
	AssistantText string                  `json:"assistant_text"`
	Operations    []aidocument.Operation  `json:"operations"`
	Summary       string                  `json:"summary"`
	Complete      bool                    `json:"complete"`
	Mutation      aidocument.ApplyRequest `json:"-"`
}

func parseProviderResponse(
	raw string,
	document aidocument.DocumentIdentity,
	locale aidocument.Locale,
	documentRevision aidocument.Revision,
	targetRevision *aidocument.Revision,
) (providerResponse, error) {
	if len(raw) == 0 || len(raw) > maxProviderResponse {
		return providerResponse{}, errors.New("provider response size is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var response providerResponse
	if err := decoder.Decode(&response); err != nil {
		return providerResponse{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return providerResponse{}, err
	}
	if len(response.AssistantText) > maxProviderResponse || len(response.Summary) > 500 {
		return providerResponse{}, errors.New("provider response field exceeds its limit")
	}
	if response.Operations == nil {
		return providerResponse{}, errors.New("provider response requires an operations array")
	}
	if response.Complete {
		if len(response.Operations) != 0 {
			return providerResponse{}, errors.New("completed provider response cannot propose operations")
		}
		return response, nil
	}
	if len(response.Operations) == 0 {
		return providerResponse{}, errors.New("incomplete provider response requires operations")
	}
	response.Mutation = aidocument.ApplyRequest{
		Protocol: aidocument.ProtocolVersion, Profile: document.Domain, Document: document.Reference,
		Locale: locale, ExpectedDocumentRevision: documentRevision,
		ExpectedTargetRevision: cloneRevision(targetRevision), Operations: response.Operations,
	}
	if _, err := aidocument.EncodeApplyRequest(response.Mutation); err != nil {
		return providerResponse{}, fmt.Errorf("validate provider operations: %w", err)
	}
	return response, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	if err := decoder.Decode(&struct{}{}); err == nil {
		return errors.New("multiple JSON values are not allowed")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func validateStartRequest(request *managev1.StartAIEditorTurnRequest) (aidocument.DocumentIdentity, aidocument.Locale, error) {
	if request == nil || request.Document == nil || request.Locale == nil {
		return aidocument.DocumentIdentity{}, "", errs.InvalidArgumentMsg("document and locale are required")
	}
	domain, ok := domainFromProto(request.Document.Domain)
	if !ok {
		return aidocument.DocumentIdentity{}, "", errs.InvalidArgument("document.domain", "is unsupported")
	}
	if request.Document.Reference == "" || request.ExpectedDocumentRevision == "" {
		return aidocument.DocumentIdentity{}, "", errs.InvalidArgumentMsg("document reference and expected document revision are required")
	}
	if !validAction(request.Action) {
		return aidocument.DocumentIdentity{}, "", errs.InvalidArgument("action", "must be a canonical lowercase action code")
	}
	if len(request.GetPrompt()) > maxPromptBytes || len(request.BlockHandles) > maxContextBlocks {
		return aidocument.DocumentIdentity{}, "", errs.InvalidArgumentMsg("prompt or block selection exceeds its limit")
	}
	seen := make(map[string]struct{}, len(request.BlockHandles))
	for _, handle := range request.BlockHandles {
		if handle == "" {
			return aidocument.DocumentIdentity{}, "", errs.InvalidArgument("block_handles", "must contain stable handles")
		}
		if _, duplicate := seen[handle]; duplicate {
			return aidocument.DocumentIdentity{}, "", errs.InvalidArgument("block_handles", "must not contain duplicates")
		}
		seen[handle] = struct{}{}
	}
	return aidocument.DocumentIdentity{Domain: domain, Reference: aidocument.DocumentReference(request.Document.Reference)}, aidocument.Locale(request.Locale.Code), nil
}

func acceptedProviderPrompt(result aidocument.ApplyResult) string {
	if result.TargetRevision == nil {
		return fmt.Sprintf(
			`The approved DCDP mutation was applied once. The authoritative document revision is %q and there is no target revision. Finish with a concise assistant message.`,
			result.DocumentRevision,
		)
	}
	return fmt.Sprintf(
		`The approved DCDP mutation was applied once. The authoritative document revision is %q and the authoritative target revision is %q. Finish with a concise assistant message.`,
		result.DocumentRevision,
		*result.TargetRevision,
	)
}

func optionalRevision(value *string) *aidocument.Revision {
	if value == nil {
		return nil
	}
	revision := aidocument.Revision(*value)
	return &revision
}

func cloneRevision(value *aidocument.Revision) *aidocument.Revision {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sameRevision(left, right *aidocument.Revision) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func validAction(value string) bool {
	if value == "" || len(value) > 80 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}

func browserPrincipal(ctx context.Context) (principal, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated {
		return principal{}, errs.AuthenticationRequired()
	}
	if user.Banned {
		return principal{}, errs.AccountBanned()
	}
	if !user.Onboarded || user.IdentityID == "" || user.MemberID == "" || user.SessionID == "" {
		return principal{}, errs.InvalidSession()
	}
	return principal{identity: user.IdentityID, member: user.MemberID, session: user.SessionID}, nil
}

func (s *Service) reserveTurn(id string, turn *activeTurn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) >= maxActiveTurns {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many active AI editor turns"))
	}
	s.turns[id] = turn
	return nil
}

func (s *Service) removeTurn(id string, turn *activeTurn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns[id] == turn {
		delete(s.turns, id)
	}
}

func (s *Service) setPending(id string, turn *activeTurn, pending *pendingToolCall) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns[id] != turn || turn.cancelled {
		return false
	}
	turn.pending = pending
	return true
}

func (s *Service) clearPending(id string, turn *activeTurn, pending *pendingToolCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turns[id] == turn && turn.pending == pending {
		turn.pending = nil
	}
}

func (s *Service) resolve(owner principal, request *managev1.ResolveAIEditorToolCallRequest) error {
	if request.TurnId == "" || request.ToolCallId == "" {
		return errs.InvalidArgumentMsg("turn_id and tool_call_id are required")
	}
	if request.Decision != managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_APPROVE &&
		request.Decision != managev1.AIEditorToolCallDecision_AI_EDITOR_TOOL_CALL_DECISION_DENY {
		return errs.InvalidArgument("decision", "must be approve or deny")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, exists := s.turns[request.TurnId]
	if !exists {
		return errs.NotFoundMsg("AI editor turn not found")
	}
	if !turn.owner.equal(owner) {
		return errs.PermissionDenied("AI editor turn belongs to another browser session")
	}
	if turn.cancelled {
		return errs.FailedPrecondition("AI editor turn is cancelled")
	}
	if turn.pending == nil || turn.pending.id != request.ToolCallId {
		return errs.FailedPrecondition("AI editor tool call is not awaiting a decision")
	}
	if turn.pending.resolved {
		return errs.FailedPrecondition("AI editor tool call was already resolved")
	}
	turn.pending.resolved = true
	turn.pending.result <- toolResolution{decision: request.Decision}
	return nil
}

func (s *Service) readContext(ctx context.Context, document aidocument.DocumentIdentity, locale aidocument.Locale, handles []string) (aidocument.Projection, error) {
	if len(handles) != 0 {
		blocks := make([]aidocument.BlockID, 0, len(handles))
		for _, handle := range handles {
			blocks = append(blocks, aidocument.BlockID(handle))
		}
		return s.document.Read(ctx, aidocument.ReadRequest{Document: document, Locale: locale, Mode: aidocument.ReadBlocks, Blocks: blocks, Limit: maxContextBlocks})
	}
	outline, err := s.document.Read(ctx, aidocument.ReadRequest{Document: document, Locale: locale, Mode: aidocument.ReadOutline, Limit: maxContextBlocks})
	if err != nil || len(outline.Nodes) == 0 {
		return outline, err
	}
	if outline.Next != nil {
		return aidocument.Projection{}, errors.New("document exceeds bounded AI context")
	}
	blocks := make([]aidocument.BlockID, 0, len(outline.Nodes))
	for _, node := range outline.Nodes {
		blocks = append(blocks, node.ID)
	}
	return s.document.Read(ctx, aidocument.ReadRequest{Document: document, Locale: locale, Mode: aidocument.ReadBlocks, Blocks: blocks, Limit: maxContextBlocks})
}

func initialProviderPrompt(request *managev1.StartAIEditorTurnRequest, projection []byte) string {
	var builder strings.Builder
	builder.WriteString(providerOperationGrammar)
	builder.WriteByte('\n')
	builder.WriteString("Action: ")
	builder.WriteString(request.Action)
	if request.GetPrompt() != "" {
		builder.WriteString("\nUser prompt: ")
		builder.WriteString(request.GetPrompt())
	}
	builder.WriteString("\nDCDP context: ")
	builder.Write(projection)
	return builder.String()
}

func providerTurnKind(index int) string {
	if index == 0 {
		return "initial"
	}
	return "tool_result"
}

func emitTerminalFailure(terminal func(managev1.AIEditorTurnTerminalStatus, managev1.AIEditorTurnFailureCode) error, code managev1.AIEditorTurnFailureCode) error {
	return terminal(managev1.AIEditorTurnTerminalStatus_AI_EDITOR_TURN_TERMINAL_STATUS_FAILED, code)
}

func closeProviderSession(session llm.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	_ = session.Close(ctx)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// Compile-time assertions keep the central registration delta mechanical.
var _ managev1connect.AIEditorOrchestrationServiceHandler = (*Service)(nil)
var _ DocumentService = (*aidocument.Service)(nil)
