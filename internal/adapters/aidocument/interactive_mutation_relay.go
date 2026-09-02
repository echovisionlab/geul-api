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
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
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
		return fmt.Errorf("validate interactive AI document mutation relay: %w", err)
	}

	_, err := relay.client.RelayInteractiveAIDocumentMutation(ctx, connect.NewRequest(request))
	if err != nil {
		return fmt.Errorf("relay committed interactive AI document mutation: %w", err)
	}
	return nil
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
