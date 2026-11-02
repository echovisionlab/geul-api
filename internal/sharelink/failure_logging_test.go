package sharelink_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// failingStore fails the link proof read and the target lock with err, as
// an unreachable database does.
type failingStore struct {
	*sharelinktest.Store
	err error
}

func (s failingStore) LiveLink(context.Context, string, sharelink.EntityType, string, time.Time) (sharelink.Link, error) {
	return sharelink.Link{}, s.err
}

func (s failingStore) LockTarget(context.Context, sharelink.EntityType, string) (sharelink.Target, error) {
	return sharelink.Target{}, s.err
}

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(sharedtelemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &output
}

// Callers decide how to answer; the error carries the redacted cause, the
// entity and the server-fault mark for the one failure record.
func TestVerifyLinkFailureCarriesCauseWithoutCredentials(t *testing.T) {
	const token = "private-share-token"
	const password = "private-share-password"
	const entityID = "11111111-1111-4111-8111-111111111111"
	cause := errors.New(`query "share_link": connection reset for ` + token + " " + password)
	output := captureDefaultLog(t)

	_, err := sharelink.VerifyLink(t.Context(), failingStore{Store: sharelinktest.NewStore(), err: cause}, token, password, sharelink.EntityPage, entityID)
	require.ErrorIs(t, err, cause)
	require.Empty(t, output.String())
	failure := failurecause.Of(err)
	require.True(t, failure.ServerFault)
	require.Equal(t, `load share link: query "share_link": connection reset for [redacted] [redacted]`, failure.Cause)
	require.Contains(t, failure.Attrs, slog.String("entity_id", entityID))
	require.Contains(t, failure.Attrs, slog.String("entity_type", string(sharelink.EntityPage)))
}

func TestDeleteFailureCarriesCauseAndEntityWithoutStoredCredentials(t *testing.T) {
	const entityID = "11111111-1111-4111-8111-111111111111"
	const token = "stored-private-share-token"
	const hash = "stored-private-password-hash"
	store := sharelinktest.NewStore()
	stored := hash
	link := store.SeedLink(sharelink.Link{
		Token: token, EntityType: sharelink.EntityPage, EntityID: entityID, PasswordHash: &stored,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
	cause := errors.New(`delete from "share_link": connection reset for ` + token + " " + hash)
	f := newFixtureOver(t, store, failingStore{Store: store, err: cause})
	output := captureDefaultLog(t)

	response, err := sharelink.NewConnectHandler(f.links).DeleteShareLink(callerContext(t), connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: link.ID}))
	require.Nil(t, response)
	require.ErrorIs(t, err, cause)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	// The request record logs the failure from what the error carries.
	require.Empty(t, output.String())
	failure := failurecause.Of(err)
	require.Equal(t, `internal: delete from "share_link": connection reset for [redacted] [redacted]`, failure.Cause)
	attrs := map[string]string{}
	for _, attr := range failure.Attrs {
		attrs[attr.Key] = attr.Value.String()
	}
	require.Equal(t, map[string]string{"share_link_id": link.ID, "entity_id": entityID, "entity_type": string(sharelink.EntityPage)}, attrs)
}
