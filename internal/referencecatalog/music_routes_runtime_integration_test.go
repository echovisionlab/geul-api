//go:build integration

package referencecatalog

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1connect "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	openv1connect "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

// Exercise cmd/server's real registration, not a separately assembled test mux.
// These contracts survived the split while their handlers disappeared.
func TestMusicRoutesRegisteredInRunningAPIIntegration(t *testing.T) {
	stack := testutil.SetupRuntimeStack(t)
	stack.StartBackend(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	member := stack.CreateUser(t, policyv1.Role.User().ID())
	client := &http.Client{Timeout: 15 * time.Second}
	request := func(t *testing.T, path string, user *testutil.OryUser, status int) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, stack.BackendURL+path, bytes.NewBufferString(`{}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if user != nil {
			testutil.ApplyAuthHeaders(req.Header, user)
		}
		response, err := client.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, status, response.StatusCode, "%s: %s", path, body)
	}
	for _, path := range []string{
		managev1connect.ArtistServiceListArtistsAdminProcedure,
		managev1connect.LabelServiceListLabelsAdminProcedure,
		managev1connect.ReleaseServiceListReleasesAdminProcedure,
	} {
		t.Run(path, func(t *testing.T) {
			request(t, path, admin, http.StatusOK)
			request(t, path, member, http.StatusForbidden)
			request(t, path, nil, http.StatusUnauthorized)
		})
	}
	for _, path := range []string{
		managev1connect.GenreServiceListGenresProcedure,
		managev1connect.StyleServiceListStylesProcedure,
		managev1connect.FormatServiceListFormatsProcedure,
	} {
		request(t, path, admin, http.StatusOK)
	}
	request(t, managev1connect.TrackServiceListTracksByReleaseProcedure, admin, http.StatusBadRequest)
	for _, path := range []string{
		openv1connect.ArtistServiceListProcedure,
		openv1connect.LabelServiceListProcedure,
		openv1connect.ReleaseServiceListProcedure,
	} {
		request(t, path, nil, http.StatusOK)
	}
}
