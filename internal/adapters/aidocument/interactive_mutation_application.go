package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/protobuf/proto"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/auth"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// InteractiveMutationApplicationPort is the full core capability shared by
// MCP and the first-party editor AI transport.
type InteractiveMutationApplicationPort interface {
	Open(context.Context, core.OpenRequest) (core.OpenMetadata, error)
	Read(context.Context, core.ReadRequest) (core.Projection, error)
	Validate(context.Context, core.ApplyRequest) (core.ValidationResult, error)
	Apply(context.Context, core.ApplyRequest) (core.ApplyResult, error)
}

// InteractiveMutationSignalPublisher emits the one exact fallback fence when
// a committed mutation could not be relayed to Collab.
type InteractiveMutationSignalPublisher interface {
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// InteractiveMutationApplication decorates only interactive MCP/editor AI
// mutations. The raw AIDocument Connect surface remains undecorated.
type InteractiveMutationApplication struct {
	application InteractiveMutationApplicationPort
	relay       *InteractiveMutationRelay
	publisher   InteractiveMutationSignalPublisher
	origin      intrav1.InteractiveAIDocumentMutationOrigin
}

func NewInteractiveMutationApplication(
	application InteractiveMutationApplicationPort,
	relay *InteractiveMutationRelay,
	publisher InteractiveMutationSignalPublisher,
	origin intrav1.InteractiveAIDocumentMutationOrigin,
) (*InteractiveMutationApplication, error) {
	if interfaceNil(application) {
		return nil, errors.New("interactive AI document application is required")
	}
	if relay == nil {
		return nil, errors.New("interactive AI document relay is required")
	}
	if interfaceNil(publisher) {
		return nil, errors.New("interactive AI document fallback publisher is required")
	}
	if origin != intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_MCP &&
		origin != intrav1.InteractiveAIDocumentMutationOrigin_INTERACTIVE_AI_DOCUMENT_MUTATION_ORIGIN_IN_EDITOR_AI {
		return nil, errors.New("interactive AI document origin must be MCP or in-editor AI")
	}
	return &InteractiveMutationApplication{
		application: application,
		relay:       relay,
		publisher:   publisher,
		origin:      origin,
	}, nil
}

func (application *InteractiveMutationApplication) Open(
	ctx context.Context,
	request core.OpenRequest,
) (core.OpenMetadata, error) {
	return application.application.Open(ctx, request)
}

func (application *InteractiveMutationApplication) Read(
	ctx context.Context,
	request core.ReadRequest,
) (core.Projection, error) {
	return application.application.Read(ctx, request)
}

func (application *InteractiveMutationApplication) Validate(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	return application.application.Validate(ctx, request)
}

func (application *InteractiveMutationApplication) Apply(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	memberID, err := authenticatedMemberID(ctx)
	if err != nil {
		return core.ApplyResult{}, err
	}
	result, err := application.application.Apply(
		core.WithInteractivePostCommitCompletion(ctx),
		request,
	)
	if err != nil || !result.Changed {
		return result, err
	}

	postCommitCtx := context.WithoutCancel(ctx)
	relayErr := application.relay.RelayCommitted(postCommitCtx, AcceptedInteractiveMutation{
		Origin:               application.origin,
		Request:              request,
		Result:               result,
		NormalizedOperations: result.Normalized,
		ActorMemberID:        memberID,
	})
	if relayErr == nil {
		return result, nil
	}

	event, eventErr := interactiveMutationFallbackEvent(request, result, memberID)
	if eventErr == nil {
		eventErr = application.publisher.NotifyProtobuf(
			core.WithInteractiveFallbackSignal(postCommitCtx),
			eventpkg.SignalContentUpdated,
			event,
		)
	}
	if eventErr != nil {
		slog.ErrorContext(
			postCommitCtx,
			"Interactive AI document relay and fallback signal failed",
			"domain", request.Profile,
			"entity_id", request.Document,
			"locale", request.Locale,
			"relay_error", relayErr,
			"fallback_error", eventErr,
		)
	}
	return result, nil
}

func authenticatedMemberID(ctx context.Context) (auth.MemberID, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || !user.Onboarded || user.MemberID == "" {
		return "", errors.New("interactive AI document mutation requires an authenticated Member")
	}
	return user.MemberID, nil
}

func interactiveMutationFallbackEvent(
	request core.ApplyRequest,
	result core.ApplyResult,
	memberID auth.MemberID,
) (*managev1.ContentUpdatedEvent, error) {
	entityType, err := interactiveContentEntityType(request.Profile)
	if err != nil {
		return nil, err
	}
	documentRevision := string(result.DocumentRevision)
	locale := string(request.Locale)
	localeExists := true
	documentStateChanged := result.TargetRevision == nil
	var targetRevision *string
	if result.TargetRevision != nil {
		value := string(*result.TargetRevision)
		targetRevision = &value
		documentStateChanged = false
	} else if isInteractiveTranslationDelete(result.Normalized) {
		localeExists = false
		documentStateChanged = false
	}
	return &managev1.ContentUpdatedEvent{
		EntityType:           entityType,
		EntityId:             string(request.Document),
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		ChangedFields:        []*managev1.ContentUpdatedField{{Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT}},
		DocumentRevision:     &documentRevision,
		ContributorMemberIds: []string{memberID.String()},
		DocumentStateChanged: documentStateChanged,
		TimestampMs:          time.Now().UnixMilli(),
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TargetRevision:       targetRevision,
	}, nil
}

func isInteractiveTranslationDelete(operations []core.Operation) bool {
	return len(operations) == 1 && operations[0].Kind == core.OperationDeleteTranslation
}

func interactiveContentEntityType(domain core.Domain) (managev1.ContentEntityType, error) {
	entityType, exists := map[core.Domain]managev1.ContentEntityType{
		core.DomainPost:          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		core.DomainPage:          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		core.DomainWork:          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		core.DomainProgramEvent:  managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT,
		core.DomainMenu:          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU,
		core.DomainEmailTemplate: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE,
		core.DomainEmailLayout:   managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_LAYOUT,
		core.DomainCampaign:      managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN,
		core.DomainForm:          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM,
		core.DomainPrivacy:       managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY,
		core.DomainTerms:         managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS,
		core.DomainPostSeries:    managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES,
	}[domain]
	if !exists {
		return managev1.ContentEntityType_CONTENT_ENTITY_TYPE_UNSPECIFIED, fmt.Errorf("unsupported interactive AI document domain %q", domain)
	}
	return entityType, nil
}
