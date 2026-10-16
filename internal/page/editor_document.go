package page

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// GetPageEditorDocument delegates authorization and materialization to room bootstrap.
func (s *PageService) GetPageEditorDocument(ctx context.Context, req *connect.Request[managev1.GetPageEditorDocumentRequest]) (_ *connect.Response[managev1.GetPageEditorDocumentResponse], returnErr error) {
	defer func() {
		if returnErr == nil || req == nil {
			return
		}
		level := slog.LevelWarn
		switch connect.CodeOf(returnErr) {
		case connect.CodeUnknown, connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss, connect.CodeDeadlineExceeded:
			level = slog.LevelError
		}
		slog.Log(ctx, level, "content operation failed", "domain", "page", "operation", "page.GetPageEditorDocument", "outcome", "failed", "page_id", req.Msg.GetPageId(), failurecause.Attr(returnErr))
	}()

	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.SessionID == "" {
		return nil, errs.AuthenticationRequired()
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	loader := &InternalPageService{db: s.db, spiceDB: s.spiceDB, contentBlocks: s.contentBlocks, mediaHydrator: s.mediaHydrator}
	loaded, err := loader.LoadPageBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPageBlockDocumentRequest{
		PageId: req.Msg.PageId, Locale: req.Msg.Locale, Principal: &intrav1.CollaborationPrincipal{SessionId: string(user.SessionID)},
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFound("page", req.Msg.PageId)
		}
		return nil, err
	}
	body := loaded.Msg
	metadata := body.LocaleMetadata
	if metadata == nil {
		metadata = body.SourceMetadata
	}
	return connect.NewResponse(&managev1.GetPageEditorDocumentResponse{
		Document: body.Document, DocumentRevision: body.DocumentRevision, Locale: body.Locale,
		LocaleExists: body.LocaleExists, TargetRevision: body.TargetRevision, SourceLocale: body.SourceMetadata.GetLocale(),
		BlockMedia: body.BlockMedia, Editable: true, DocumentLayout: body.DocumentLayout,
		Title: metadata.Title, Summary: metadata.Summary,
	}), nil
}
