package oauthapp

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the OAuth app Connect RPCs.
type ConnectHandler struct {
	apps *Service
}

var _ managev1connect.OAuthAppServiceHandler = (*ConnectHandler)(nil)

// NewConnectHandler returns the Connect adapter of the OAuth app use cases.
func NewConnectHandler(apps *Service) *ConnectHandler {
	return &ConnectHandler{apps: apps}
}

func (h *ConnectHandler) ListSignInApps(ctx context.Context, _ *connect.Request[managev1.ListSignInAppsRequest]) (*connect.Response[managev1.ListSignInAppsResponse], error) {
	apps, err := h.apps.ListSignInApps(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	response := &managev1.ListSignInAppsResponse{Apps: make([]*managev1.SignInApp, len(apps))}
	for i, app := range apps {
		response.Apps[i] = SignInAppMessage(app)
	}
	return connect.NewResponse(response), nil
}

func (h *ConnectHandler) GetSignInApp(ctx context.Context, req *connect.Request[managev1.GetSignInAppRequest]) (*connect.Response[managev1.SignInApp], error) {
	app, err := h.apps.GetSignInApp(ctx, req.Msg.GetClientId())
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(SignInAppMessage(app)), nil
}

func (h *ConnectHandler) CreateSignInApp(ctx context.Context, req *connect.Request[managev1.CreateSignInAppRequest]) (*connect.Response[managev1.CreateSignInAppResponse], error) {
	saved, err := h.apps.CreateSignInApp(ctx, CreateCommand{ClientID: req.Msg.GetClientId(), Settings: SettingsFromMessage(req.Msg.GetSettings())})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.CreateSignInAppResponse{App: SignInAppMessage(saved.App), ClientSecret: saved.Secret}), nil
}

func (h *ConnectHandler) UpdateSignInApp(ctx context.Context, req *connect.Request[managev1.UpdateSignInAppRequest]) (*connect.Response[managev1.UpdateSignInAppResponse], error) {
	saved, err := h.apps.UpdateSignInApp(ctx, UpdateCommand{
		ClientID: req.Msg.GetClientId(), ExpectedRevision: req.Msg.GetExpectedRevision(), Settings: SettingsFromMessage(req.Msg.GetSettings()),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.UpdateSignInAppResponse{App: SignInAppMessage(saved.App), ClientSecret: saved.Secret}), nil
}

func (h *ConnectHandler) DeleteSignInApp(ctx context.Context, req *connect.Request[managev1.DeleteSignInAppRequest]) (*connect.Response[managev1.DeleteSignInAppResponse], error) {
	if err := h.apps.DeleteSignInApp(ctx, AppCommand{ClientID: req.Msg.GetClientId(), ExpectedRevision: req.Msg.GetExpectedRevision()}); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.DeleteSignInAppResponse{}), nil
}

func (h *ConnectHandler) RotateSignInAppSecret(ctx context.Context, req *connect.Request[managev1.RotateSignInAppSecretRequest]) (*connect.Response[managev1.RotateSignInAppSecretResponse], error) {
	saved, err := h.apps.RotateSignInAppSecret(ctx, AppCommand{ClientID: req.Msg.GetClientId(), ExpectedRevision: req.Msg.GetExpectedRevision()})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.RotateSignInAppSecretResponse{App: SignInAppMessage(saved.App), ClientSecret: *saved.Secret}), nil
}

func (h *ConnectHandler) ListConnectedTools(ctx context.Context, _ *connect.Request[managev1.ListConnectedToolsRequest]) (*connect.Response[managev1.ListConnectedToolsResponse], error) {
	tools, err := h.apps.ListConnectedTools(ctx)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	response := &managev1.ListConnectedToolsResponse{Tools: make([]*managev1.ConnectedTool, len(tools))}
	for i, tool := range tools {
		response.Tools[i] = ConnectedToolMessage(tool)
	}
	return connect.NewResponse(response), nil
}

func (h *ConnectHandler) DisconnectConnectedTool(ctx context.Context, req *connect.Request[managev1.DisconnectConnectedToolRequest]) (*connect.Response[managev1.DisconnectConnectedToolResponse], error) {
	if err := h.apps.DisconnectConnectedTool(ctx, req.Msg.GetClientId()); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.DisconnectConnectedToolResponse{}), nil
}

func (h *ConnectHandler) AuthorizeOAuthSignIn(ctx context.Context, req *connect.Request[managev1.AuthorizeOAuthSignInRequest]) (*connect.Response[managev1.AuthorizeOAuthSignInResponse], error) {
	decision, err := h.apps.AuthorizeOAuthSignIn(ctx, req.Msg.GetClientId())
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.AuthorizeOAuthSignInResponse{Decision: decisionMessage(decision)}), nil
}

// SignInAppMessage is the transport message of app.
func SignInAppMessage(app App) *managev1.SignInApp {
	settings := app.Settings
	message := &managev1.SignInApp{
		ClientId:               app.ClientID,
		Name:                   settings.Name,
		Access:                 managev1.SignInAccess(managev1.SignInAccess_value[string(settings.Access)]),
		RedirectUris:           settings.RedirectURIs,
		PostLogoutRedirectUris: settings.PostLogoutRedirectURIs,
		AuthMethod:             authMethodMessage(settings.AuthMethod),
		AccessTokenLifespan:    lifespanMessage(settings.AccessTokenLifespan),
		IdTokenLifespan:        lifespanMessage(settings.IDTokenLifespan),
		Revision:               app.Revision,
		CreatedAt:              timestamppb.New(app.CreatedAt),
	}
	if app.UpdatedAt != nil {
		message.UpdatedAt = timestamppb.New(*app.UpdatedAt)
	}
	for _, scope := range settings.Scopes {
		message.Scopes = append(message.Scopes, scopeMessage(scope))
	}
	for _, criteria := range app.MemberCriteria {
		message.MemberCriteria = append(message.MemberCriteria, &managev1.MemberCriteriaSummary{
			Id: criteria.ID, Name: criteria.Name, Description: criteria.Description,
			Kind: managev1.MemberCriteriaKind(managev1.MemberCriteriaKind_value[criteria.Kind]),
		})
	}
	return message
}

// SettingsFromMessage reads the settings an Admin chose; values it does not
// know are left for normalizeSettings to reject.
func SettingsFromMessage(message *managev1.SignInAppSettings) Settings {
	settings := Settings{
		Name:                   message.GetName(),
		MemberCriteriaIDs:      message.GetMemberCriteriaIds(),
		RedirectURIs:           message.GetRedirectUris(),
		PostLogoutRedirectURIs: message.GetPostLogoutRedirectUris(),
		AccessTokenLifespan:    lifespanOf(message.GetAccessTokenLifespan()),
		IDTokenLifespan:        lifespanOf(message.GetIdTokenLifespan()),
	}
	if access := message.GetAccess(); access != managev1.SignInAccess_SIGN_IN_ACCESS_UNSPECIFIED {
		settings.Access = Access(access.String())
	}
	switch message.GetAuthMethod() {
	case managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_CLIENT_SECRET_BASIC:
		settings.AuthMethod = AuthMethodClientSecretBasic
	case managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_CLIENT_SECRET_POST:
		settings.AuthMethod = AuthMethodClientSecretPost
	case managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_NONE:
		settings.AuthMethod = AuthMethodNone
	}
	for _, scope := range message.GetScopes() {
		switch scope {
		case managev1.SignInAppScope_SIGN_IN_APP_SCOPE_EMAIL:
			settings.Scopes = append(settings.Scopes, ScopeEmail)
		case managev1.SignInAppScope_SIGN_IN_APP_SCOPE_PROFILE:
			settings.Scopes = append(settings.Scopes, ScopeProfile)
		default:
			settings.Scopes = append(settings.Scopes, Scope(scope.String()))
		}
	}
	return settings
}

// ConnectedToolMessage is the transport message of tool.
func ConnectedToolMessage(tool ConnectedTool) *managev1.ConnectedTool {
	message := &managev1.ConnectedTool{
		ClientId: tool.ClientID, Name: tool.Name, Publisher: tool.Publisher, Stale: tool.Stale,
		RedirectUris: tool.RedirectURIs, Resource: tool.Resource, AuthMethod: tool.AuthMethod,
	}
	if tool.CreatedAt != nil {
		message.CreatedAt = timestamppb.New(*tool.CreatedAt)
	}
	return message
}

func authMethodMessage(method AuthMethod) managev1.SignInAppAuthMethod {
	switch method {
	case AuthMethodClientSecretBasic:
		return managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_CLIENT_SECRET_BASIC
	case AuthMethodClientSecretPost:
		return managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_CLIENT_SECRET_POST
	case AuthMethodNone:
		return managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_NONE
	default:
		return managev1.SignInAppAuthMethod_SIGN_IN_APP_AUTH_METHOD_UNSPECIFIED
	}
}

func scopeMessage(scope Scope) managev1.SignInAppScope {
	if scope == ScopeEmail {
		return managev1.SignInAppScope_SIGN_IN_APP_SCOPE_EMAIL
	}
	return managev1.SignInAppScope_SIGN_IN_APP_SCOPE_PROFILE
}

var lifespanMessages = map[time.Duration]managev1.SignInTokenLifespan{
	5 * time.Minute:  managev1.SignInTokenLifespan_SIGN_IN_TOKEN_LIFESPAN_FIVE_MINUTES,
	15 * time.Minute: managev1.SignInTokenLifespan_SIGN_IN_TOKEN_LIFESPAN_FIFTEEN_MINUTES,
	time.Hour:        managev1.SignInTokenLifespan_SIGN_IN_TOKEN_LIFESPAN_ONE_HOUR,
}

func lifespanMessage(lifespan time.Duration) managev1.SignInTokenLifespan {
	return lifespanMessages[lifespan]
}

func lifespanOf(message managev1.SignInTokenLifespan) time.Duration {
	for lifespan, candidate := range lifespanMessages {
		if candidate == message {
			return lifespan
		}
	}
	return 0
}

func decisionMessage(decision Decision) managev1.SignInDecision {
	switch decision {
	case DecisionNotSignInApp:
		return managev1.SignInDecision_SIGN_IN_DECISION_NOT_SIGN_IN_APP
	case DecisionAllowed:
		return managev1.SignInDecision_SIGN_IN_DECISION_ALLOWED
	default:
		return managev1.SignInDecision_SIGN_IN_DECISION_DENIED
	}
}
