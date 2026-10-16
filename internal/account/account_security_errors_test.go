package account

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

type emailChangeLookupIdentityManager struct {
	accountIdentityManager
	lookupErr error
}

func (m emailChangeLookupIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return nil, m.lookupErr
}

func TestRequestEmailChangePreservesIdentityLookupFailures(t *testing.T) {
	unavailable := errors.New("identity service unavailable")
	for _, tt := range []struct {
		name string
		err  error
		code connect.Code
	}{
		{"missing", nil, connect.CodeNotFound},
		{"HTTP not found", &auth.KratosHTTPError{StatusCode: http.StatusNotFound}, connect.CodeNotFound},
		{"HTTP unavailable", &auth.KratosHTTPError{StatusCode: http.StatusServiceUnavailable, Body: "private-upstream-response session_token=private-token"}, connect.CodeInternal},
		{"connection failure", unavailable, connect.CodeInternal},
		{"deadline", context.DeadlineExceeded, connect.CodeDeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			service := &AccountService{identity: emailChangeLookupIdentityManager{lookupErr: tt.err}}
			ctx := auth.WithUser(t.Context(), &auth.UserInfo{IdentityID: "11111111-1111-4111-8111-111111111111", MemberID: "22222222-2222-4222-8222-222222222222", SessionID: "33333333-3333-4333-8333-333333333333", Authenticated: true, AuthenticatedAt: time.Now().UTC()})
			response, err := service.RequestEmailChange(ctx, connect.NewRequest(&managev1.RequestEmailChangeRequest{NewEmail: "next@example.test"}))
			require.Nil(t, response)
			require.Equal(t, tt.code, connect.CodeOf(err))
			if tt.err != nil && tt.code == connect.CodeInternal {
				require.ErrorIs(t, err, tt.err)
				var rpcError *connect.Error
				require.ErrorAs(t, err, &rpcError)
				require.Equal(t, "identity lookup failed", rpcError.Message())
				require.NotContains(t, err.Error(), "private-upstream-response")
				require.NotContains(t, err.Error(), "private-token")
			}
		})
	}
}
