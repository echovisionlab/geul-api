package account

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// ConnectHandler serves the Account Connect RPCs except the personal access
// token ones, which internal/adapters/account decorates it with. Each method
// translates the request into a use-case command or query, and the result
// into the response; errs.Wrap maps use-case errors to Connect codes.
type ConnectHandler struct {
	managev1connect.UnimplementedAccountServiceHandler
	accounts *Service
}

// NewConnectHandler returns the Connect adapter of accounts.
func NewConnectHandler(accounts *Service) *ConnectHandler {
	return &ConnectHandler{accounts: accounts}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func securityResponse(security *managev1.AccountSecurity, err error, sessionsRevoked bool) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	return respond(&managev1.AccountSecurityMutationResponse{Security: security, SessionsRevoked: sessionsRevoked}, err)
}

func newsletterResponse(state *managev1.NewsletterSubscriptionState, err error) (*connect.Response[managev1.NewsletterSubscriptionMutationResponse], error) {
	return respond(&managev1.NewsletterSubscriptionMutationResponse{NewsletterSubscription: state}, err)
}

// accountRole maps the API role; an unknown one is the zero role, which
// SetAccountRole rejects after its authorization checks.
func accountRole(role policyv1.AuthorizationRole) policyv1.RoleID {
	switch role {
	case policyv1.AuthorizationRole_USER:
		return policyv1.Role.User()
	case policyv1.AuthorizationRole_AUTHOR:
		return policyv1.Role.Author()
	case policyv1.AuthorizationRole_ADMIN:
		return policyv1.Role.Admin()
	default:
		return policyv1.RoleID{}
	}
}

func (h *ConnectHandler) GetMySecurity(ctx context.Context, _ *connect.Request[managev1.GetMySecurityRequest]) (*connect.Response[managev1.GetMySecurityResponse], error) {
	security, err := h.accounts.GetMySecurity(ctx)
	return respond(&managev1.GetMySecurityResponse{Security: security}, err)
}

func (h *ConnectHandler) SetMyCanonicalEmail(ctx context.Context, req *connect.Request[managev1.SetMyCanonicalEmailRequest]) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	security, err := h.accounts.SetMyCanonicalEmail(ctx, SetMyCanonicalEmailCommand{Email: req.Msg.Email})
	return securityResponse(security, err, false)
}

func (h *ConnectHandler) RequestEmailChange(ctx context.Context, req *connect.Request[managev1.RequestEmailChangeRequest]) (*connect.Response[managev1.RequestEmailChangeResponse], error) {
	availability, err := h.accounts.RequestEmailChange(ctx, RequestEmailChangeCommand{NewEmail: req.Msg.NewEmail})
	return respond(&managev1.RequestEmailChangeResponse{Success: availability.Available, Message: availability.Message}, err)
}

func (h *ConnectHandler) RevokeMySession(ctx context.Context, req *connect.Request[managev1.RevokeMySessionRequest]) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	security, err := h.accounts.RevokeMySession(ctx, RevokeMySessionCommand{SessionID: req.Msg.SessionId})
	return securityResponse(security, err, true)
}

func (h *ConnectHandler) RevokeMyOtherSessions(ctx context.Context, _ *connect.Request[managev1.RevokeMyOtherSessionsRequest]) (*connect.Response[managev1.AccountSecurityMutationResponse], error) {
	security, err := h.accounts.RevokeMyOtherSessions(ctx)
	return securityResponse(security, err, true)
}

func (h *ConnectHandler) RequestAccountDeletion(ctx context.Context, _ *connect.Request[managev1.RequestAccountDeletionRequest]) (*connect.Response[managev1.RequestAccountDeletionResponse], error) {
	result, err := h.accounts.RequestAccountDeletion(ctx)
	message := "A confirmation email has been sent. Please check your email to confirm account deletion."
	if result.AlreadyScheduled {
		message = "Account deletion is already scheduled. Check your email for the confirmation link."
	}
	return respond(&managev1.RequestAccountDeletionResponse{Success: true, Message: message}, err)
}

func (h *ConnectHandler) SetMyNewsletterSubscription(ctx context.Context, req *connect.Request[managev1.SetMyNewsletterSubscriptionRequest]) (*connect.Response[managev1.NewsletterSubscriptionMutationResponse], error) {
	return newsletterResponse(h.accounts.SetMyNewsletterSubscription(ctx, SetMyNewsletterSubscriptionCommand{Subscribed: req.Msg.GetSubscribed()}))
}

func (h *ConnectHandler) SetAccountRole(ctx context.Context, req *connect.Request[managev1.SetAccountRoleRequest]) (*connect.Response[managev1.AccountSummary], error) {
	return respond(h.accounts.SetAccountRole(ctx, SetAccountRoleCommand{MemberID: req.Msg.MemberId, Role: accountRole(req.Msg.Role)}))
}

func (h *ConnectHandler) BanAccount(ctx context.Context, req *connect.Request[managev1.BanAccountRequest]) (*connect.Response[managev1.AccountSummary], error) {
	command := BanAccountCommand{MemberID: req.Msg.MemberId, Reason: req.Msg.Reason}
	if req.Msg.Until != nil {
		until := req.Msg.Until.AsTime()
		command.Until = &until
	}
	return respond(h.accounts.BanAccount(ctx, command))
}

func (h *ConnectHandler) UnbanAccount(ctx context.Context, req *connect.Request[managev1.UnbanAccountRequest]) (*connect.Response[managev1.AccountSummary], error) {
	return respond(h.accounts.UnbanAccount(ctx, UnbanAccountCommand{MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) SetAccountCanonicalEmail(ctx context.Context, req *connect.Request[managev1.SetAccountCanonicalEmailRequest]) (*connect.Response[managev1.AccountSummary], error) {
	return respond(h.accounts.SetAccountCanonicalEmail(ctx, SetAccountCanonicalEmailCommand{MemberID: req.Msg.MemberId, Email: req.Msg.Email}))
}

func (h *ConnectHandler) RemoveAccountSsoProvider(ctx context.Context, req *connect.Request[managev1.RemoveAccountSsoProviderRequest]) (*connect.Response[managev1.AccountSummary], error) {
	return respond(h.accounts.RemoveAccountSsoProvider(ctx, RemoveAccountSsoProviderCommand{
		MemberID: req.Msg.MemberId, Provider: req.Msg.Provider, Identifier: req.Msg.Identifier,
	}))
}

func (h *ConnectHandler) DeleteAccount(ctx context.Context, req *connect.Request[managev1.DeleteAccountRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, h.accounts.DeleteAccount(ctx, DeleteAccountCommand{MemberID: req.Msg.MemberId}))
}

func (h *ConnectHandler) UnsubscribeAccountFromNewsletter(ctx context.Context, req *connect.Request[managev1.UnsubscribeAccountFromNewsletterRequest]) (*connect.Response[managev1.NewsletterSubscriptionMutationResponse], error) {
	err := h.accounts.UnsubscribeAccountFromNewsletter(ctx, UnsubscribeAccountFromNewsletterCommand{MemberID: req.Msg.GetMemberId()})
	return newsletterResponse(&managev1.NewsletterSubscriptionState{}, err)
}
