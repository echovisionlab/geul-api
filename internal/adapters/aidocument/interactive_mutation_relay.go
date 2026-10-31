package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	contractcollaboration "github.com/echovisionlab/geul-event-contracts/go/collaboration"
)

// InteractiveMutationRelayClient is the consumer-owned outbound boundary to
// editor-collab. Implementations must make one RPC attempt per invocation and
// must not add a retry ledger or durable relay history.
type InteractiveMutationRelayClient interface {
	RelayInteractiveAIDocumentMutation(
		context.Context,
		*connect.Request[intrav1.RelayInteractiveAIDocumentMutationRequest],
	) (*connect.Response[intrav1.RelayInteractiveAIDocumentMutationResponse], error)
}

// AcceptedInteractiveMutation contains only facts available after the owning
// domain transaction has committed. NormalizedOperations must be the exact
// canonical operation batch returned by that transaction, never the original
// unvalidated request payload.
type AcceptedInteractiveMutation struct {
	Origin               intrav1.InteractiveAIDocumentMutationOrigin
	Request              aidocument.ApplyRequest
	Result               aidocument.ApplyResult
	NormalizedOperations []aidocument.Operation
	ActorMemberID        auth.MemberID
}

// InteractiveMutationRelay converts one committed interactive mutation to the
// shared Event Contracts wire and performs one best-effort RPC attempt.
type InteractiveMutationRelay struct {
	client        InteractiveMutationRelayClient
	newMutationID func() string
}

func NewInteractiveMutationRelay(client InteractiveMutationRelayClient) (*InteractiveMutationRelay, error) {
	if interfaceNil(client) {
		return nil, errors.New("interactive AI document mutation relay client is required")
	}
	return &InteractiveMutationRelay{client: client, newMutationID: uuid.NewString}, nil
}

// RelayCommitted performs no persistence and no retry. A caller must invoke it
// only after Apply returned a changed result and the owning transaction
// committed. Rejected, failed, conflicting, and semantic no-op mutations fail
// closed before the outbound client is called.
func (relay *InteractiveMutationRelay) RelayCommitted(
	ctx context.Context,
	mutation AcceptedInteractiveMutation,
) error {
	if relay == nil || interfaceNil(relay.client) || relay.newMutationID == nil {
		return errors.New("interactive AI document mutation relay is not configured")
	}
	if !mutation.Result.Changed {
		return errors.New("interactive AI document mutation relay requires a changed committed result")
	}
	if len(mutation.NormalizedOperations) == 0 {
		return errors.New("interactive AI document mutation relay requires committed normalized operations")
	}
	if strings.TrimSpace(mutation.ActorMemberID.String()) == "" {
		return errors.New("interactive AI document mutation relay requires an authenticated Member")
	}

	request := &intrav1.RelayInteractiveAIDocumentMutationRequest{
		MutationId:               relay.newMutationID(),
		Origin:                   mutation.Origin,
		Document:                 documentToProto(mutation.Request.Identity()),
		Locale:                   localeToProto(mutation.Request.Locale),
		ExpectedDocumentRevision: string(mutation.Request.ExpectedDocumentRevision),
		AcceptedDocumentRevision: string(mutation.Result.DocumentRevision),
		Operations:               operationsToProto(mutation.NormalizedOperations),
		ActorMemberId:            mutation.ActorMemberID.String(),
	}
	if mutation.Request.ExpectedTargetRevision != nil {
		expected := string(*mutation.Request.ExpectedTargetRevision)
		request.ExpectedTargetRevision = &expected
	}
	if mutation.Result.TargetRevision != nil {
		accepted := string(*mutation.Result.TargetRevision)
		request.AcceptedTargetRevision = &accepted
	}
	if err := contractcollaboration.ValidateRelayInteractiveAIDocumentMutationRequest(request); err != nil {
		return newInteractiveMutationRelayError(request, relayStageValidate, fmt.Errorf("validate interactive AI document mutation relay: %w", err))
	}

	_, err := relay.client.RelayInteractiveAIDocumentMutation(ctx, connect.NewRequest(request))
	if err != nil {
		return newInteractiveMutationRelayError(request, relayStageRPC, fmt.Errorf("relay committed interactive AI document mutation: %w", err))
	}
	return nil
}

const (
	relayStageValidate = "validate"
	relayStageRPC      = "rpc"
)

// InteractiveMutationRelayError describes one failed relay attempt using only
// bounded identifiers. Collab logs its own "Interactive mutation relay failed"
// record with the same mutation_id, and its Connect handler replaces
// non-Connect errors with "internal error", so mutation_id is how an operator
// finds the real cause on the Collab side.
type InteractiveMutationRelayError struct {
	MutationID     string
	Stage          string
	OperationCount int
	OperationKinds string
	err            error
}

func newInteractiveMutationRelayError(
	request *intrav1.RelayInteractiveAIDocumentMutationRequest,
	stage string,
	err error,
) *InteractiveMutationRelayError {
	return &InteractiveMutationRelayError{
		MutationID:     request.GetMutationId(),
		Stage:          stage,
		OperationCount: len(request.GetOperations()),
		OperationKinds: relayOperationKinds(request.GetOperations()),
		err:            err,
	}
}

func (e *InteractiveMutationRelayError) Error() string { return e.err.Error() }

func (e *InteractiveMutationRelayError) Unwrap() error { return e.err }

// logAttrs adds the Connect code and whether Collab answered (wire error) or
// the call failed before a response, such as a dial or timeout error.
func (e *InteractiveMutationRelayError) logAttrs() []any {
	attrs := []any{
		"mutation_id", e.MutationID,
		"relay_stage", e.Stage,
		"operation_count", e.OperationCount,
		"operation_kinds", e.OperationKinds,
	}
	if e.Stage == relayStageRPC {
		attrs = append(attrs,
			"connect_code", errs.CodeOf(e.err).String(),
			"wire_error", connect.IsWireError(e.err),
		)
	}
	return attrs
}

// relayOperationKinds lists the distinct operation kinds by their wire names
// (for example "set_field") in first-seen order. Values are never included.
func relayOperationKinds(operations []*managev1.AIDocumentOperation) string {
	seen := map[string]struct{}{}
	kinds := make([]string, 0, len(operations))
	for _, operation := range operations {
		message := operation.ProtoReflect()
		field := message.WhichOneof(message.Descriptor().Oneofs().ByName("operation"))
		kind := "unknown"
		if field != nil {
			kind = string(field.Name())
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	return strings.Join(kinds, "; ")
}

func interfaceNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
