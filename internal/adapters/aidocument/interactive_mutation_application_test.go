package aidocumentadapter

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/stretchr/testify/require"
)

type interactiveApplicationStub struct {
	result   core.ApplyResult
	err      error
	applyCtx context.Context
	calls    int
}

func (*interactiveApplicationStub) Open(context.Context, core.OpenRequest) (core.OpenMetadata, error) {
	return core.OpenMetadata{}, nil
}

func (*interactiveApplicationStub) Read(context.Context, core.ReadRequest) (core.Projection, error) {
	return core.Projection{}, nil
}

func (*interactiveApplicationStub) Validate(context.Context, core.ApplyRequest) (core.ValidationResult, error) {
	return core.ValidationResult{}, nil
}

func (application *interactiveApplicationStub) Apply(ctx context.Context, _ core.ApplyRequest) (core.ApplyResult, error) {
	application.calls++
	application.applyCtx = ctx
	return application.result, application.err
}

type interactiveFallbackPublisher struct {
	signals  []string
	messages []proto.Message
	contexts []context.Context
	err      error
}

func (publisher *interactiveFallbackPublisher) NotifyProtobuf(
	ctx context.Context,
	signal string,
	message proto.Message,
) error {
	publisher.contexts = append(publisher.contexts, ctx)
	publisher.signals = append(publisher.signals, signal)
	publisher.messages = append(publisher.messages, message)
	return publisher.err
}

func interactiveApplicationFixture(
	t *testing.T,
	result core.ApplyResult,
	relayErr error,
	publisher *interactiveFallbackPublisher,
	origin intrav1.InteractiveAIDocumentMutationOrigin,
) (*InteractiveMutationApplication, *interactiveApplicationStub, *recordingInteractiveMutationRelayClient) {
	t.Helper()
	application := &interactiveApplicationStub{result: result}
	client := &recordingInteractiveMutationRelayClient{err: relayErr}
	relay, err := NewInteractiveMutationRelay(client)
	require.NoError(t, err)
	relay.newMutationID = func() string { return "019cd2ba-aabe-7fec-ac92-aef81b16565b" }
	decorated, err := NewInteractiveMutationApplication(application, relay, publisher, origin)
	require.NoError(t, err)
	return decorated, application, client
}

func interactiveMemberContext() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		Authenticated: true,
		Onboarded:     true,
		MemberID:      auth.MemberID("22222222-2222-4222-8222-222222222222"),
	})
}

func interactiveSourceRequest(domain core.Domain) core.ApplyRequest {
	return core.ApplyRequest{
		Protocol:                 core.ProtocolVersion,
		Profile:                  domain,
		Document:                 core.DocumentReference("11111111-1111-4111-8111-111111111111"),
		Locale:                   core.Locale("ko"),
		ExpectedDocumentRevision: core.Revision("document-before"),
		Operations:               []core.Operation{core.DeleteBlockOperation("block-1")},
	}
}

func interactiveChangedSourceResult() core.ApplyResult {
	operation := core.DeleteBlockOperation("block-1")
	return core.ApplyResult{
		DocumentRevision: core.Revision("document-after"),
		Changed:          true,
		Changes:          []core.Change{{Operation: 0, Kind: core.OperationDeleteBlock}},
		Normalized:       []core.Operation{operation},
	}
}

func TestInteractiveMutationApplicationRelaySuccessSuppressesFallback(t *testing.T) {
	t.Parallel()
	publisher := &interactiveFallbackPublisher{}
	decorated, application, relayClient := interactiveApplicationFixture(
		t,
		interactiveChangedSourceResult(),
		nil,
		publisher,
		intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
	)

	result, err := decorated.Apply(interactiveMemberContext(), interactiveSourceRequest(core.DomainPost))

	require.NoError(t, err)
	require.True(t, result.Changed)
	require.True(t, core.InteractivePostCommitCompletionOwnsSignal(application.applyCtx))
	require.Len(t, relayClient.requests, 1)
	require.Equal(t, intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP, relayClient.requests[0].GetOrigin())
	require.Empty(t, publisher.messages)
}

func TestInteractiveMutationApplicationRelayFailurePublishesOneExactFallback(t *testing.T) {
	t.Parallel()
	targetBefore := core.Revision("target-before")
	targetAfter := core.Revision("target-after")
	request := interactiveSourceRequest(core.DomainCampaign)
	request.Locale = "en"
	request.ExpectedTargetRevision = &targetBefore
	result := interactiveChangedSourceResult()
	result.DocumentRevision = request.ExpectedDocumentRevision
	result.TargetRevision = &targetAfter
	publisher := &interactiveFallbackPublisher{}
	decorated, _, relayClient := interactiveApplicationFixture(
		t,
		result,
		errors.New("relay unavailable"),
		publisher,
		intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_IN_EDITOR_AI,
	)

	accepted, err := decorated.Apply(interactiveMemberContext(), request)

	require.NoError(t, err, "post-commit delivery failure must not make the caller retry the committed mutation")
	require.Equal(t, result, accepted)
	require.Len(t, relayClient.requests, 1)
	require.Equal(t, []string{eventpkg.SignalContentUpdated}, publisher.signals)
	require.False(t, core.InteractivePostCommitCompletionOwnsSignal(publisher.contexts[0]))
	event, ok := publisher.messages[0].(*managev1.ContentUpdatedEvent)
	require.True(t, ok)
	require.Equal(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN, event.GetEntityType())
	require.Equal(t, request.Document, core.DocumentReference(event.GetEntityId()))
	require.Equal(t, "en", event.GetLocale())
	require.True(t, event.GetLocaleExists())
	require.NotNil(t, event.LocaleExists)
	require.Equal(t, "document-before", event.GetDocumentRevision())
	require.Equal(t, "target-after", event.GetTargetRevision())
	require.False(t, event.GetDocumentStateChanged())
	require.Equal(t, []string{"22222222-2222-4222-8222-222222222222"}, event.GetContributorMemberIds())
}

func TestInteractiveMutationApplicationFallbackDistinguishesSourceAndTargetDeletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		request             core.ApplyRequest
		result              core.ApplyResult
		wantLocaleExists    bool
		wantDocumentChanged bool
		wantTargetRevision  string
	}{
		{
			name:                "source",
			request:             interactiveSourceRequest(core.DomainPage),
			result:              interactiveChangedSourceResult(),
			wantLocaleExists:    true,
			wantDocumentChanged: true,
		},
		{
			name: "target deletion",
			request: func() core.ApplyRequest {
				request := interactiveSourceRequest(core.DomainPage)
				request.Locale = "en"
				target := core.Revision("target-before")
				request.ExpectedTargetRevision = &target
				request.Operations = []core.Operation{core.DeleteTranslationOperation()}
				return request
			}(),
			result: core.ApplyResult{
				DocumentRevision: "document-before",
				Changed:          true,
				Changes:          []core.Change{{Operation: 0, Kind: core.OperationDeleteTranslation}},
				Normalized:       []core.Operation{core.DeleteTranslationOperation()},
			},
			wantLocaleExists: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			publisher := &interactiveFallbackPublisher{}
			decorated, _, _ := interactiveApplicationFixture(
				t,
				test.result,
				errors.New("relay unavailable"),
				publisher,
				intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
			)

			_, err := decorated.Apply(interactiveMemberContext(), test.request)
			require.NoError(t, err)
			event := publisher.messages[0].(*managev1.ContentUpdatedEvent)
			require.Equal(t, test.wantLocaleExists, event.GetLocaleExists())
			require.Equal(t, test.wantDocumentChanged, event.GetDocumentStateChanged())
			require.Equal(t, test.wantTargetRevision, event.GetTargetRevision())
		})
	}
}

func TestInteractiveMutationApplicationNoopAndRejectedMutationHaveNoPostCommitIO(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result core.ApplyResult
		err    error
	}{
		{name: "semantic no-op", result: core.ApplyResult{DocumentRevision: "document-before", Normalized: []core.Operation{core.DeleteBlockOperation("block-1")}}},
		{name: "rejected", err: errors.New("conflict")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			publisher := &interactiveFallbackPublisher{}
			decorated, application, relayClient := interactiveApplicationFixture(
				t,
				test.result,
				nil,
				publisher,
				intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
			)
			application.err = test.err

			_, _ = decorated.Apply(interactiveMemberContext(), interactiveSourceRequest(core.DomainPost))

			require.Empty(t, relayClient.requests)
			require.Empty(t, publisher.messages)
		})
	}
}

func TestInteractiveMutationApplicationRequiresMemberBeforeMutation(t *testing.T) {
	t.Parallel()
	publisher := &interactiveFallbackPublisher{}
	decorated, application, relayClient := interactiveApplicationFixture(
		t,
		interactiveChangedSourceResult(),
		nil,
		publisher,
		intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP,
	)

	_, err := decorated.Apply(context.Background(), interactiveSourceRequest(core.DomainPost))

	require.Error(t, err)
	require.Zero(t, application.calls)
	require.Empty(t, relayClient.requests)
	require.Empty(t, publisher.messages)
}

func TestInteractiveMutationFallbackCatalogCoversEverySupportedDomain(t *testing.T) {
	t.Parallel()
	seen := make(map[managev1.ContentEntityType]struct{})
	for _, domain := range core.SupportedDomains() {
		entityType, err := interactiveContentEntityType(domain)
		require.NoError(t, err, domain)
		require.NotEqual(t, managev1.ContentEntityType_CONTENT_ENTITY_TYPE_UNSPECIFIED, entityType)
		seen[entityType] = struct{}{}
	}
	require.Len(t, seen, len(core.SupportedDomains()))
}

var _ InteractiveMutationApplicationPort = (*interactiveApplicationStub)(nil)
var _ InteractiveMutationSignalPublisher = (*interactiveFallbackPublisher)(nil)
var _ InteractiveMutationRelayClient = (*recordingInteractiveMutationRelayClient)(nil)
