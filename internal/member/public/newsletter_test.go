package public

import (
	"testing"

	"connectrpc.com/connect"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The token is the only authority: an invalid one answers before any
// database work. Replays and the audited transition are integration tests.
func TestNewsletterUnsubscribeRejectsAnInvalidTokenBeforeTheDatabase(t *testing.T) {
	handler := NewNewsletterConnectHandler(newTestService(t, Deps{DB: &gorm.DB{}}))

	_, err := handler.Unsubscribe(t.Context(), connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: "invalid"}))

	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "token: must be a valid unsubscribe token")
}
