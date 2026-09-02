package ai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func TestServiceDoesNotExposeLegacyChatProcedure(t *testing.T) {
	t.Parallel()

	servicePath, handler := managev1connect.NewAIServiceHandler(
		NewService(&MetadataJobManager{}),
	)
	request := httptest.NewRequest(
		http.MethodPost,
		servicePath+"Chat",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	require.Equal(t, http.StatusNotFound, response.Code)
}
