package accountadapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/member/pat"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PersonalAccessTokenHandler decorates the existing AccountService handler and
// owns only the Account PAT transport boundary. Persistence remains behind the
// Member-owned pat.Repository interface.
type PersonalAccessTokenHandler struct {
	managev1connect.AccountServiceHandler
	tokens *pat.Service
}

// NewPersonalAccessTokenHandler composes the existing AccountService RPCs with
// the one Member-owned PAT application service assembled by the server.
func NewPersonalAccessTokenHandler(
	delegate managev1connect.AccountServiceHandler,
	tokens *pat.Service,
) (*PersonalAccessTokenHandler, error) {
	if delegate == nil {
		return nil, fmt.Errorf("account service handler is required")
	}
	if tokens == nil {
		return nil, fmt.Errorf("personal access token service is required")
	}
	return &PersonalAccessTokenHandler{
		AccountServiceHandler: delegate,
		tokens:                tokens,
	}, nil
}

func (handler *PersonalAccessTokenHandler) CreateMyPersonalAccessToken(
	ctx context.Context,
	_ *connect.Request[managev1.CreateMyPersonalAccessTokenRequest],
) (*connect.Response[managev1.CreateMyPersonalAccessTokenResponse], error) {
	principal, err := requireFreshBrowserSecurityPrincipal(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return handler.createPersonalAccessToken(ctx, pat.MemberID(principal.MemberID))
}

func (handler *PersonalAccessTokenHandler) CreateAccountPersonalAccessToken(
	ctx context.Context,
	req *connect.Request[managev1.CreateAccountPersonalAccessTokenRequest],
) (*connect.Response[managev1.CreateMyPersonalAccessTokenResponse], error) {
	if _, err := requireFreshBrowserSecurityPrincipal(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	memberID, err := accountPersonalAccessTokenMemberID(req.Msg.GetMemberId())
	if err != nil {
		return nil, err
	}
	return handler.createPersonalAccessToken(ctx, memberID)
}

func (handler *PersonalAccessTokenHandler) createPersonalAccessToken(
	ctx context.Context,
	memberID pat.MemberID,
) (*connect.Response[managev1.CreateMyPersonalAccessTokenResponse], error) {
	issued, err := handler.tokens.Create(ctx, memberID)
	if err != nil {
		return nil, personalAccessTokenServiceError(err, "", "")
	}
	metadata, err := personalAccessTokenMetadataProto(issued.Metadata)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.CreateMyPersonalAccessTokenResponse{
		PersonalAccessToken: metadata,
		Secret:              issued.Secret.Reveal(),
	}), nil
}

func (handler *PersonalAccessTokenHandler) ListMyPersonalAccessTokens(
	ctx context.Context,
	_ *connect.Request[managev1.ListMyPersonalAccessTokensRequest],
) (*connect.Response[managev1.ListMyPersonalAccessTokensResponse], error) {
	principal, err := requireBrowserSecurityPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	return handler.listPersonalAccessTokens(ctx, pat.MemberID(principal.MemberID))
}

func (handler *PersonalAccessTokenHandler) ListAccountPersonalAccessTokens(
	ctx context.Context,
	req *connect.Request[managev1.ListAccountPersonalAccessTokensRequest],
) (*connect.Response[managev1.ListMyPersonalAccessTokensResponse], error) {
	if _, err := requireBrowserSecurityPrincipal(ctx); err != nil {
		return nil, err
	}
	memberID, err := accountPersonalAccessTokenMemberID(req.Msg.GetMemberId())
	if err != nil {
		return nil, err
	}
	return handler.listPersonalAccessTokens(ctx, memberID)
}

func (handler *PersonalAccessTokenHandler) listPersonalAccessTokens(
	ctx context.Context,
	memberID pat.MemberID,
) (*connect.Response[managev1.ListMyPersonalAccessTokensResponse], error) {
	metadata, err := handler.tokens.List(ctx, memberID)
	if err != nil {
		return nil, personalAccessTokenServiceError(err, "", "")
	}
	response := &managev1.ListMyPersonalAccessTokensResponse{
		PersonalAccessTokens: make([]*managev1.PersonalAccessToken, 0, len(metadata)),
	}
	for _, token := range metadata {
		item, err := personalAccessTokenMetadataProto(token)
		if err != nil {
			return nil, err
		}
		response.PersonalAccessTokens = append(response.PersonalAccessTokens, item)
	}
	return connect.NewResponse(response), nil
}

func (handler *PersonalAccessTokenHandler) RegenerateMyPersonalAccessToken(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateMyPersonalAccessTokenRequest],
) (*connect.Response[managev1.RegenerateMyPersonalAccessTokenResponse], error) {
	principal, err := requireFreshBrowserSecurityPrincipal(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return handler.regeneratePersonalAccessToken(
		ctx,
		pat.MemberID(principal.MemberID),
		req.Msg.GetPersonalAccessTokenId(),
	)
}

func (handler *PersonalAccessTokenHandler) RegenerateAccountPersonalAccessToken(
	ctx context.Context,
	req *connect.Request[managev1.RegenerateAccountPersonalAccessTokenRequest],
) (*connect.Response[managev1.RegenerateMyPersonalAccessTokenResponse], error) {
	if _, err := requireFreshBrowserSecurityPrincipal(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	memberID, err := accountPersonalAccessTokenMemberID(req.Msg.GetMemberId())
	if err != nil {
		return nil, err
	}
	return handler.regeneratePersonalAccessToken(ctx, memberID, req.Msg.GetPersonalAccessTokenId())
}

func (handler *PersonalAccessTokenHandler) regeneratePersonalAccessToken(
	ctx context.Context,
	memberID pat.MemberID,
	rawTokenID string,
) (*connect.Response[managev1.RegenerateMyPersonalAccessTokenResponse], error) {
	tokenID := pat.TokenID(rawTokenID)
	issued, err := handler.tokens.Regenerate(ctx, memberID, tokenID)
	if err != nil {
		return nil, personalAccessTokenServiceError(err, "personal_access_token_id", rawTokenID)
	}
	metadata, err := personalAccessTokenMetadataProto(issued.Metadata)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.RegenerateMyPersonalAccessTokenResponse{
		PersonalAccessToken: metadata,
		Secret:              issued.Secret.Reveal(),
	}), nil
}

func (handler *PersonalAccessTokenHandler) DeleteMyPersonalAccessToken(
	ctx context.Context,
	req *connect.Request[managev1.DeleteMyPersonalAccessTokenRequest],
) (*connect.Response[managev1.DeleteMyPersonalAccessTokenResponse], error) {
	principal, err := requireFreshBrowserSecurityPrincipal(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return handler.deletePersonalAccessToken(
		ctx,
		pat.MemberID(principal.MemberID),
		req.Msg.GetPersonalAccessTokenId(),
	)
}

func (handler *PersonalAccessTokenHandler) DeleteAccountPersonalAccessToken(
	ctx context.Context,
	req *connect.Request[managev1.DeleteAccountPersonalAccessTokenRequest],
) (*connect.Response[managev1.DeleteMyPersonalAccessTokenResponse], error) {
	if _, err := requireFreshBrowserSecurityPrincipal(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	memberID, err := accountPersonalAccessTokenMemberID(req.Msg.GetMemberId())
	if err != nil {
		return nil, err
	}
	return handler.deletePersonalAccessToken(ctx, memberID, req.Msg.GetPersonalAccessTokenId())
}

func (handler *PersonalAccessTokenHandler) deletePersonalAccessToken(
	ctx context.Context,
	memberID pat.MemberID,
	rawTokenID string,
) (*connect.Response[managev1.DeleteMyPersonalAccessTokenResponse], error) {
	if err := handler.tokens.Delete(ctx, memberID, pat.TokenID(rawTokenID)); err != nil {
		return nil, personalAccessTokenServiceError(err, "personal_access_token_id", rawTokenID)
	}
	return connect.NewResponse(&managev1.DeleteMyPersonalAccessTokenResponse{Deleted: true}), nil
}

func accountPersonalAccessTokenMemberID(raw string) (pat.MemberID, error) {
	if _, err := uuidutil.ParseCanonical(raw, "member_id"); err != nil {
		return "", errs.InvalidArgument("member_id", "must be a canonical UUID")
	}
	return pat.MemberID(raw), nil
}

func requireFreshBrowserSecurityPrincipal(ctx context.Context, now time.Time) (*auth.UserInfo, error) {
	principal, err := requireBrowserSecurityPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if !auth.IsFreshForSecurityMutation(principal, now) {
		return nil, errs.FailedPrecondition("reauthenticate before changing account security settings")
	}
	return principal, nil
}

func requireBrowserSecurityPrincipal(ctx context.Context) (*auth.UserInfo, error) {
	principal, err := authorizationtarget.RequireAuthenticatedPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	// Machine-bearer dispatch intentionally creates no browser Session.
	// Reject it independently of freshness so a caller cannot manufacture an
	// AuthenticatedAt value and turn a bearer into an Account security session.
	if principal.SessionID == "" {
		return nil, errs.InvalidSession()
	}
	return principal, nil
}

func personalAccessTokenMetadataProto(metadata pat.Metadata) (*managev1.PersonalAccessToken, error) {
	createdAt := timestamppb.New(metadata.CreatedAt)
	if err := createdAt.CheckValid(); err != nil {
		return nil, errs.Internal(fmt.Errorf("stored personal access token has an invalid creation time"))
	}
	return &managev1.PersonalAccessToken{
		Id:        string(metadata.ID),
		CreatedAt: createdAt,
	}, nil
}

func personalAccessTokenServiceError(err error, invalidField, tokenID string) error {
	switch {
	case errors.Is(err, pat.ErrInvalidInput):
		if invalidField != "" {
			return errs.InvalidArgument(invalidField, "invalid identifier")
		}
		return errs.Internal(fmt.Errorf("personal access token service rejected server-derived input"))
	case errors.Is(err, pat.ErrTokenAlreadyExists):
		return errs.AlreadyExistsMsg("personal access token already exists")
	case errors.Is(err, pat.ErrTokenNotFound):
		return errs.NotFound("personal access token", tokenID)
	default:
		return errs.Internal(err)
	}
}

var _ managev1connect.AccountServiceHandler = (*PersonalAccessTokenHandler)(nil)
