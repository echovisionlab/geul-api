package referencecatalog

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestListMapPlacesAdminRequiresAuthenticatedPrincipalBeforeDatabase(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal *auth.UserInfo
		code      connect.Code
	}{
		{"anonymous", nil, connect.CodeUnauthenticated},
		{"unauthenticated", &auth.UserInfo{MemberID: "11111111-1111-4111-8111-111111111111", IdentityID: "22222222-2222-4222-8222-222222222222"}, connect.CodeUnauthenticated},
		{"unlinked", &auth.UserInfo{Authenticated: true, IdentityID: "22222222-2222-4222-8222-222222222222"}, connect.CodeUnauthenticated},
		{"banned", &auth.UserInfo{Authenticated: true, MemberID: "11111111-1111-4111-8111-111111111111", IdentityID: "22222222-2222-4222-8222-222222222222", Banned: true}, connect.CodePermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if test.principal != nil {
				ctx = auth.WithUser(ctx, test.principal)
			}
			_, err := (&MapPlaceService{}).ListMapPlacesAdmin(ctx, connect.NewRequest(&managev1.ListMapPlacesAdminRequest{}))
			require.Error(t, err)
			require.Equal(t, test.code, connect.CodeOf(err))
		})
	}
}
