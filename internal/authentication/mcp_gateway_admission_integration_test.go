//go:build integration

package authentication

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMCPGatewayAuthorAdmissionUsesAuthorHierarchyIntegration(t *testing.T) {
	stack, err := testutil.StartBackendIntegrationStack(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stack.Close()) })
	handler, err := NewMCPGatewayAuthorAdmissionHandler(
		testMCPAdmissionSecret,
		testMCPAuthHeaderName,
		testMCPInternalHeaderName,
		stack.SpiceDBClient,
	)
	require.NoError(t, err)

	for _, test := range []struct {
		name   string
		role   policyv1.RoleID
		status int
	}{
		{name: "author", role: policyv1.Role.Author(), status: http.StatusOK},
		{name: "admin inherits author", role: policyv1.Role.Admin(), status: http.StatusOK},
		{name: "user denied", role: policyv1.Role.User(), status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			identityID := uuid.NewString()
			subject, subjectErr := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
			require.NoError(t, subjectErr)
			_, syncErr := stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, test.role)
			require.NoError(t, syncErr)

			response := httptest.NewRecorder()
			handler.ServeHTTP(response, newMCPAdmissionRequest(validMCPAdmissionBody(identityID)))
			require.Equal(t, test.status, response.Code)
			require.Empty(t, response.Body.String())
		})
	}
}
