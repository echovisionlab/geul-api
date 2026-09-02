package aidocumentadapter

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type recordingInteractiveMutationRelayClient struct {
	requests []*intrav1.RelayInteractiveAIDocumentMutationRequest
	err      error
}

func (client *recordingInteractiveMutationRelayClient) RelayInteractiveAIDocumentMutation(
	_ context.Context,
	request *connect.Request[intrav1.RelayInteractiveAIDocumentMutationRequest],
) (*connect.Response[intrav1.RelayInteractiveAIDocumentMutationResponse], error) {
	client.requests = append(client.requests, request.Msg)
	if client.err != nil {
		return nil, client.err
	}
	return connect.NewResponse(&intrav1.RelayInteractiveAIDocumentMutationResponse{}), nil
}

func TestInteractiveMutationRelayUsesExactCommittedNormalizedBatch(t *testing.T) {
	t.Parallel()

	client := &recordingInteractiveMutationRelayClient{}
	relay, err := NewInteractiveMutationRelay(client)
	if err != nil {
		t.Fatal(err)
	}
	relay.newMutationID = func() string { return "mutation-1" }

	raw := aidocument.SetFieldOperation("block-1", "content", aidocument.Text("raw"))
	normalized := aidocument.SetFieldOperation("block-1", "content", aidocument.Text("canonical"))
	mutation := AcceptedInteractiveMutation{
		Origin: intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
		Request: aidocument.ApplyRequest{
			Protocol: aidocument.ProtocolVersion, Profile: aidocument.DomainPost,
			Document: "post-1", Locale: "ko", ExpectedDocumentRevision: "document-before",
			Operations: []aidocument.Operation{raw},
		},
		Result: aidocument.ApplyResult{
			DocumentRevision: "document-after", Changed: true,
			Changes: []aidocument.Change{{Operation: 0, Kind: aidocument.OperationSetField}},
		},
		NormalizedOperations: []aidocument.Operation{normalized},
		ActorMemberID:        auth.MemberID("member-1"),
	}

	if err := relay.RelayCommitted(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("relay calls = %d, want 1", len(client.requests))
	}
	request := client.requests[0]
	if request.GetMutationId() != "mutation-1" || request.GetActorMemberId() != "member-1" {
		t.Fatalf("identity tuple = %q/%q", request.GetMutationId(), request.GetActorMemberId())
	}
	if request.GetDocument().GetDomain() != managev1.AIDocumentDomain_AI_DOCUMENT_DOMAIN_POST || request.GetDocument().GetReference() != "post-1" || request.GetLocale().GetCode() != "ko" {
		t.Fatalf("document tuple = %+v/%+v", request.GetDocument(), request.GetLocale())
	}
	if request.GetExpectedDocumentRevision() != "document-before" || request.GetAcceptedDocumentRevision() != "document-after" || request.ExpectedTargetRevision != nil || request.AcceptedTargetRevision != nil {
		t.Fatalf("revision tuple = %+v", request)
	}
	wantOperations := operationsToProto([]aidocument.Operation{normalized})
	if !reflect.DeepEqual(request.GetOperations(), wantOperations) {
		t.Fatalf("relayed operations = %+v, want normalized %+v", request.GetOperations(), wantOperations)
	}
	if reflect.DeepEqual(request.GetOperations(), operationsToProto([]aidocument.Operation{raw})) {
		t.Fatal("relay used the original unnormalized request operation")
	}
}

func TestInteractiveMutationRelayPreservesTargetRevisionTuple(t *testing.T) {
	t.Parallel()

	client := &recordingInteractiveMutationRelayClient{}
	relay, err := NewInteractiveMutationRelay(client)
	if err != nil {
		t.Fatal(err)
	}
	relay.newMutationID = func() string { return "mutation-target" }
	expectedTarget := aidocument.Revision("target-before")
	acceptedTarget := aidocument.Revision("target-after")
	mutation := AcceptedInteractiveMutation{
		Origin: intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_IN_EDITOR_AI,
		Request: aidocument.ApplyRequest{
			Protocol: aidocument.ProtocolVersion, Profile: aidocument.DomainPage,
			Document: "page-1", Locale: "en", ExpectedDocumentRevision: "document-same",
			ExpectedTargetRevision: &expectedTarget,
			Operations:             []aidocument.Operation{aidocument.UnsetFieldOperation("block-1", "content")},
		},
		Result: aidocument.ApplyResult{
			DocumentRevision: "document-same", TargetRevision: &acceptedTarget, Changed: true,
			Changes: []aidocument.Change{{Operation: 0, Kind: aidocument.OperationUnsetField}},
		},
		NormalizedOperations: []aidocument.Operation{aidocument.UnsetFieldOperation("block-1", "content")},
		ActorMemberID:        auth.MemberID("member-2"),
	}

	if err := relay.RelayCommitted(t.Context(), mutation); err != nil {
		t.Fatal(err)
	}
	request := client.requests[0]
	if request.ExpectedTargetRevision == nil || *request.ExpectedTargetRevision != "target-before" || request.AcceptedTargetRevision == nil || *request.AcceptedTargetRevision != "target-after" {
		t.Fatalf("target revision tuple = %+v", request)
	}
}

func TestInteractiveMutationRelayPreservesTargetLifecycleRevisionTuples(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		operation      aidocument.Operation
		expectedTarget *aidocument.Revision
		acceptedTarget *aidocument.Revision
	}{
		{
			name:           "missing target create",
			operation:      aidocument.CreateTranslationOperation(),
			acceptedTarget: revisionPointer("target-created"),
		},
		{
			name:           "existing target delete",
			operation:      aidocument.DeleteTranslationOperation(),
			expectedTarget: revisionPointer("target-before"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &recordingInteractiveMutationRelayClient{}
			relay, err := NewInteractiveMutationRelay(client)
			if err != nil {
				t.Fatal(err)
			}
			relay.newMutationID = func() string { return "mutation-lifecycle" }
			mutation := AcceptedInteractiveMutation{
				Origin: intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
				Request: aidocument.ApplyRequest{
					Protocol: aidocument.ProtocolVersion, Profile: aidocument.DomainPost,
					Document: "post-1", Locale: "en", ExpectedDocumentRevision: "document-same",
					ExpectedTargetRevision: test.expectedTarget,
					Operations:             []aidocument.Operation{test.operation},
				},
				Result: aidocument.ApplyResult{
					DocumentRevision: "document-same", TargetRevision: test.acceptedTarget, Changed: true,
					Changes: []aidocument.Change{{Operation: 0, Kind: test.operation.Kind}},
				},
				NormalizedOperations: []aidocument.Operation{test.operation},
				ActorMemberID:        auth.MemberID("member-3"),
			}

			if err := relay.RelayCommitted(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			request := client.requests[0]
			if !equalOptionalString(request.ExpectedTargetRevision, revisionStringPointer(test.expectedTarget)) ||
				!equalOptionalString(request.AcceptedTargetRevision, revisionStringPointer(test.acceptedTarget)) {
				t.Fatalf("target lifecycle tuple = expected %v accepted %v", request.ExpectedTargetRevision, request.AcceptedTargetRevision)
			}
		})
	}
}

func TestInteractiveMutationRelayFailsClosedWithoutCallingClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*AcceptedInteractiveMutation)
	}{
		{name: "semantic no-op", mutate: func(value *AcceptedInteractiveMutation) { value.Result.Changed = false }},
		{name: "missing normalized operations", mutate: func(value *AcceptedInteractiveMutation) { value.NormalizedOperations = nil }},
		{name: "missing actor", mutate: func(value *AcceptedInteractiveMutation) { value.ActorMemberID = "" }},
		{name: "unspecified origin", mutate: func(value *AcceptedInteractiveMutation) {
			value.Origin = intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_UNSPECIFIED
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := &recordingInteractiveMutationRelayClient{}
			relay, err := NewInteractiveMutationRelay(client)
			if err != nil {
				t.Fatal(err)
			}
			relay.newMutationID = func() string { return "mutation-1" }
			mutation := validAcceptedInteractiveMutation()
			test.mutate(&mutation)
			if err := relay.RelayCommitted(t.Context(), mutation); err == nil {
				t.Fatal("invalid accepted mutation must fail closed")
			}
			if len(client.requests) != 0 {
				t.Fatalf("relay calls = %d, want 0", len(client.requests))
			}
		})
	}
}

func TestInteractiveMutationRelayAttemptsClientExactlyOnce(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("collab unavailable")
	client := &recordingInteractiveMutationRelayClient{err: wantErr}
	relay, err := NewInteractiveMutationRelay(client)
	if err != nil {
		t.Fatal(err)
	}
	relay.newMutationID = func() string { return "mutation-1" }

	err = relay.RelayCommitted(t.Context(), validAcceptedInteractiveMutation())
	if !errors.Is(err, wantErr) {
		t.Fatalf("relay error = %v, want %v", err, wantErr)
	}
	if len(client.requests) != 1 {
		t.Fatalf("relay calls = %d, want exactly 1", len(client.requests))
	}
}

func TestNewInteractiveMutationRelayRejectsNilClient(t *testing.T) {
	t.Parallel()

	if _, err := NewInteractiveMutationRelay(nil); err == nil {
		t.Fatal("nil client must be rejected")
	}
	var typedNil *recordingInteractiveMutationRelayClient
	if _, err := NewInteractiveMutationRelay(typedNil); err == nil {
		t.Fatal("typed nil client must be rejected")
	}
}

func validAcceptedInteractiveMutation() AcceptedInteractiveMutation {
	operation := aidocument.DeleteBlockOperation("block-1")
	return AcceptedInteractiveMutation{
		Origin: intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
		Request: aidocument.ApplyRequest{
			Protocol: aidocument.ProtocolVersion, Profile: aidocument.DomainPost,
			Document: "post-1", Locale: "ko", ExpectedDocumentRevision: "document-before",
			Operations: []aidocument.Operation{operation},
		},
		Result: aidocument.ApplyResult{
			DocumentRevision: "document-after", Changed: true,
			Changes: []aidocument.Change{{Operation: 0, Kind: aidocument.OperationDeleteBlock}},
		},
		NormalizedOperations: []aidocument.Operation{operation},
		ActorMemberID:        auth.MemberID("member-1"),
	}
}

func revisionPointer(value aidocument.Revision) *aidocument.Revision {
	return &value
}

func revisionStringPointer(value *aidocument.Revision) *string {
	if value == nil {
		return nil
	}
	converted := string(*value)
	return &converted
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
