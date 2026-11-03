package programevent

import (
	"context"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// InternalConnectHandler serves the collaboration service's typed Program
// Event Block boundary. Yjs state stays in editor-collab and never crosses it.
type InternalConnectHandler struct {
	events *Service
}

// NewInternalConnectHandler returns the internal Program Event Connect adapter.
func NewInternalConnectHandler(events *Service) *InternalConnectHandler {
	return &InternalConnectHandler{events: events}
}

func (h *InternalConnectHandler) LoadProgramEventBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadProgramEventBlockDocumentRequest]) (*connect.Response[intrav1.LoadProgramEventBlockDocumentResponse], error) {
	query := LoadBlockDocumentQuery{EventID: req.Msg.EventId, Locale: req.Msg.Locale}
	if req.Msg.Principal != nil {
		sessionID := req.Msg.Principal.GetSessionId()
		query.SessionID = &sessionID
	}
	room, err := h.events.LoadBlockDocument(ctx, query)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(roomDocumentMessage(room)), nil
}

func (h *InternalConnectHandler) ApplyProgramEventBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyProgramEventBlockBatchRequest]) (*connect.Response[intrav1.ApplyProgramEventBlockBatchResponse], error) {
	written, err := h.events.ApplyBlockBatch(ctx, ApplyBlockBatchCommand{
		EventID: req.Msg.EventId, Locale: req.Msg.Locale, Batch: req.Msg.Batch,
		AffectedLocaleValues: req.Msg.AffectedLocaleValues, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
	})
	return respond(&intrav1.ApplyProgramEventBlockBatchResponse{
		DocumentRevision: written.DocumentRevision, Changed: written.Changed, SourceChanged: written.SourceChanged,
		ChangedLocales: written.ChangedLocales, Locale: written.Locale, TargetRevision: written.TargetRevision,
	}, err)
}

func (h *InternalConnectHandler) UpdateProgramEventLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateProgramEventLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateProgramEventLocaleMetadataResponse], error) {
	written, err := h.events.UpdateLocaleMetadata(ctx, UpdateLocaleMetadataCommand{
		EventID: req.Msg.EventId, Locale: req.Msg.Locale, Title: req.Msg.Title, Summary: stringMutation(req.Msg.Summary),
		ExpectedRevision: req.Msg.ExpectedRevision, ContributorMemberIDs: req.Msg.ContributorMemberIds,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
	})
	return respond(&intrav1.UpdateProgramEventLocaleMetadataResponse{
		DocumentRevision: written.DocumentRevision, Changed: written.Changed, SourceChanged: written.SourceChanged,
		ChangedLocales: written.ChangedLocales, Locale: written.Locale, TargetRevision: written.TargetRevision,
	}, err)
}

func stringMutation(input *intrav1.NullableStringMutation) *entityparticipant.StringMutation {
	if input == nil {
		return nil
	}
	switch operation := input.Operation.(type) {
	case *intrav1.NullableStringMutation_Set:
		return &entityparticipant.StringMutation{Set: &operation.Set}
	case *intrav1.NullableStringMutation_Clear:
		return &entityparticipant.StringMutation{Clear: &operation.Clear}
	default:
		return &entityparticipant.StringMutation{}
	}
}
