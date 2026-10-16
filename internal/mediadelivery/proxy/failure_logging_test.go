package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mediadelivery/config"
	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
)

type failedDeliveryStore struct{ err error }

func (s failedDeliveryStore) Stat(context.Context, string) (minio.ObjectInfo, error) {
	return minio.ObjectInfo{}, s.err
}
func (s failedDeliveryStore) Open(context.Context, string, *byteRange) (io.ReadCloser, error) {
	panic("unexpected open")
}

func TestDeliveryStatFailureLogsCauseWithoutChangingNotFoundStatus(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	router := newMediaRouter(&config.Config{}, failedDeliveryStore{errors.New("object storage unavailable token=hidden")}, nil, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/asset/"+testAssetID+"/image.png", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Contains(t, entry["failure_cause"], "object storage unavailable")
	require.Equal(t, "ERROR", entry["level"])
	require.Equal(t, testAssetID, entry["asset_id"])
	require.NotContains(t, output.String(), "hidden")
}

func TestImageUpstreamFailureDoesNotLogResponseBody(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	proxy := NewImageProxy(&config.Config{})
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	resolved := &resolvedDelivery{path: mediaauth.DeliveryPath{AssetID: testAssetID}}
	proxy.writeImgproxyResponse(rec, request, resolved, &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private body password=hidden private@example.test"))})
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, output.String(), "imgproxy returned error")
	for _, private := range []string{"private body", "hidden", "private@example.test"} {
		require.NotContains(t, output.String(), private)
	}
}
