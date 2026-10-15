package auth

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// ExpectedMemberHeader names the member an editor write belongs to. Browsers keep unsaved
// edits per member; a tab whose member signed out (or was replaced by another account's
// session cookie) must never land those edits under the current session.
const ExpectedMemberHeader = "Geul-Expected-Member"

// ErrExpectedMemberMismatch reports a write made for another member than the session's.
var ErrExpectedMemberMismatch = errors.New("the session belongs to another member than this write")

// CheckExpectedMember accepts an absent expectation and otherwise requires the authenticated
// member to be exactly the expected one.
func CheckExpectedMember(ctx context.Context, expected string) error {
	if expected == "" {
		return nil
	}
	user := GetUser(ctx)
	if user == nil || !user.Authenticated || user.MemberID.String() != expected {
		return ErrExpectedMemberMismatch
	}
	return nil
}

// ExpectedMemberInterceptor runs after the auth interceptor on session-authenticated RPCs.
// A mismatch is Unauthenticated: the browser's session no longer belongs to the editor.
func ExpectedMemberInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			values := request.Header().Values(ExpectedMemberHeader)
			if len(values) > 1 {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("duplicate Geul-Expected-Member header"))
			}
			if err := CheckExpectedMember(ctx, request.Header().Get(ExpectedMemberHeader)); err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			return next(ctx, request)
		}
	})
}
