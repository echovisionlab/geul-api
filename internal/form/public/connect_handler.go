package public

import (
	"context"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/requestip"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Form RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedFormServiceHandler
	forms *Service
}

// NewConnectHandler returns the Connect adapter of forms.
func NewConnectHandler(forms *Service) *ConnectHandler {
	return &ConnectHandler{forms: forms}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) CheckAccess(ctx context.Context, req *connect.Request[openv1.CheckFormAccessRequest]) (*connect.Response[openv1.CheckFormAccessResponse], error) {
	return respond(h.forms.CheckFormAccess(ctx, CheckFormAccessQuery{
		Slug:           req.Msg.Slug,
		Dashboard:      normalizeFormAccessTarget(req.Msg.Target) == openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD,
		Embed:          normalizeFormAccessContext(req.Msg.Context) == openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED,
		ShareToken:     req.Msg.ShareToken,
		SharePassword:  req.Msg.SharePassword,
		Password:       req.Msg.Password,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) Submit(ctx context.Context, req *connect.Request[openv1.SubmitFormRequest]) (*connect.Response[openv1.SubmitFormResponse], error) {
	return respond(h.forms.SubmitForm(ctx, submitFormCommand(req)))
}

// submitFormCommand reads the answers and the client the request came from.
func submitFormCommand(req *connect.Request[openv1.SubmitFormRequest]) SubmitFormCommand {
	return SubmitFormCommand{
		FormID: req.Msg.FormId, Data: req.Msg.Data, Password: req.Msg.Password,
		ClientAddress:  requestip.TrustedClientIP(req.Header().Get("X-Forwarded-For"), req.Header().Get("X-Real-IP"), req.Peer().Addr),
		UserAgent:      req.Header().Get("User-Agent"),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}
}

func (h *ConnectHandler) VerifyPassword(ctx context.Context, req *connect.Request[openv1.VerifyFormPasswordRequest]) (*connect.Response[openv1.VerifyFormPasswordResponse], error) {
	return respond(h.forms.VerifyFormPassword(ctx, VerifyFormPasswordQuery{
		Slug: req.Msg.Slug, Password: req.Msg.Password, ShareToken: req.Msg.ShareToken, SharePassword: req.Msg.SharePassword,
	}))
}

func (h *ConnectHandler) GetDashboard(ctx context.Context, req *connect.Request[openv1.GetFormDashboardRequest]) (*connect.Response[openv1.GetFormDashboardResponse], error) {
	return respond(h.forms.GetFormDashboard(ctx, GetFormDashboardQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.ShareToken, SharePassword: req.Msg.SharePassword,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}
