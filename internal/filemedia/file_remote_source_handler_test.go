package filemedia

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func uploadSourceTestService(t *testing.T, handler http.Handler, maxSize int64) (*FileService, *recordingPostAccess) {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	access := &recordingPostAccess{}
	service := &FileService{
		postAccess: access,
		uploadConfigs: map[managev1.UploadType]*model.UploadConfig{
			managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE: {PermittedMimeTypes: []string{"image/png"}, MaxSize: maxSize},
		},
		remoteImportResolver:      &remoteImportTestResolver{ips: []net.IP{net.ParseIP("8.8.8.8")}},
		remoteImportBaseTransport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, // Local TLS fixture only.
		remoteImportDialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			require.Equal(t, "8.8.8.8:443", address)
			return new(net.Dialer).DialContext(ctx, network, upstream.Listener.Addr().String())
		},
	}
	// db, S3, processing, and async dependencies remain nil: a successful relay
	// must never access those persistence or lifecycle dependencies.
	return service, access
}

func uploadSourceTestRequest(t *testing.T) *http.Request {
	t.Helper()
	query := url.Values{
		"uploadType": {strconv.Itoa(int(managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE))},
		"entityId":   {uuid.NewString()},
		"entityType": {strconv.Itoa(int(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST))},
		"url":        {"https://media.example.com/original.jpg"},
	}
	req := httptest.NewRequest(http.MethodGet, "/upload/source?"+query.Encode(), nil)
	return req.WithContext(auth.WithUser(req.Context(), postAccessPrincipal()))
}

func uploadSourcePNG(size int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x55}, size-8)...)
}

func TestUploadSourceOriginalBytesAndMetadata(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(strconv.FormatBool(unknown), func(t *testing.T) {
			original := uploadSourcePNG(remoteImportSniffBytes + 37)
			service, access := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Empty(t, r.Header.Get("Cookie"))
				require.Empty(t, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("Set-Cookie", "upstream=private")
				w.Header().Set("Location", "https://private.example.com")
				if unknown {
					w.(http.Flusher).Flush()
				} else {
					w.Header().Set("Content-Length", strconv.Itoa(len(original)))
				}
				_, _ = w.Write(original)
			}), int64(len(original)+1))
			rec := httptest.NewRecorder()
			req := uploadSourceTestRequest(t)
			req.Header.Set("Cookie", "session=secret")
			req.Header.Set("Authorization", "Bearer secret")
			service.HandleUploadSource(rec, req)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, original, rec.Body.Bytes())
			require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
			kind, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
			require.NoError(t, err)
			require.Equal(t, "attachment", kind)
			require.Equal(t, "original.png", params["filename"])
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			require.Empty(t, rec.Header().Get("Set-Cookie"))
			require.Empty(t, rec.Header().Get("Location"))
			if unknown {
				require.Empty(t, rec.Header().Get("Content-Length"))
				require.Empty(t, rec.Header().Get("X-Upload-Source-Size"))
			} else {
				require.Equal(t, strconv.Itoa(len(original)), rec.Header().Get("Content-Length"))
				require.Equal(t, strconv.Itoa(len(original)), rec.Header().Get("X-Upload-Source-Size"))
			}
			require.Equal(t, 1, access.requireEditCount)
		})
	}
}

func TestUploadSourceRejectsBeforeOutput(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*http.Request, *FileService, *recordingPostAccess)
		want  int
	}{
		{"method", func(r *http.Request, _ *FileService, _ *recordingPostAccess) { r.Method = http.MethodPost }, 405},
		{"unauthenticated", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			*r = *r.WithContext(context.Background())
		}, 401},
		{"forbidden", func(_ *http.Request, _ *FileService, a *recordingPostAccess) { a.err = errs.PermissionDenied("denied") }, 403},
		{"invalid upload", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			q := r.URL.Query()
			q.Set("uploadType", "99999")
			r.URL.RawQuery = q.Encode()
		}, 400},
		{"invalid entity", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			q := r.URL.Query()
			q.Set("entityId", "bad")
			r.URL.RawQuery = q.Encode()
		}, 400},
		{"invalid entity enum", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			q := r.URL.Query()
			q.Set("entityType", "99999")
			r.URL.RawQuery = q.Encode()
		}, 400},
		{"duplicate url", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			r.URL.RawQuery += "&url=https%3A%2F%2Felsewhere.example.com"
		}, 400},
		{"HTTP url", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			q := r.URL.Query()
			q.Set("url", "http://media.example.com/file.png")
			r.URL.RawQuery = q.Encode()
		}, 400},
		{"credentials", func(r *http.Request, _ *FileService, _ *recordingPostAccess) {
			q := r.URL.Query()
			q.Set("url", "https://user:secret@media.example.com/file.png")
			r.URL.RawQuery = q.Encode()
		}, 400},
		{"SSRF", func(_ *http.Request, s *FileService, _ *recordingPostAccess) {
			s.remoteImportResolver = &remoteImportTestResolver{ips: []net.IP{net.ParseIP("127.0.0.1")}}
		}, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, access := uploadSourceTestService(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("upstream must not be called") }), 100)
			req := uploadSourceTestRequest(t)
			tt.alter(req, service, access)
			rec := httptest.NewRecorder()
			service.HandleUploadSource(rec, req)
			require.Equal(t, tt.want, rec.Code)
			require.Empty(t, rec.Header().Get("Content-Disposition"))
		})
	}
}

func TestUploadSourceRejectsSourceAndRedirect(t *testing.T) {
	for _, name := range []string{"known oversize", "sniffed oversize", "unsupported", "redirect"} {
		t.Run(name, func(t *testing.T) {
			service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch name {
				case "known oversize":
					w.Header().Set("Content-Length", "101")
					_, _ = w.Write(uploadSourcePNG(101))
				case "sniffed oversize":
					w.(http.Flusher).Flush()
					_, _ = w.Write(uploadSourcePNG(101))
				case "unsupported":
					_, _ = w.Write([]byte("<html>not an image</html>"))
				case "redirect":
					http.Redirect(w, r, "https://127.0.0.1/private", http.StatusFound)
				}
			}), 100)
			rec := httptest.NewRecorder()
			service.HandleUploadSource(rec, uploadSourceTestRequest(t))
			if name == "redirect" {
				require.Equal(t, 502, rec.Code)
			} else {
				require.Equal(t, 400, rec.Code)
			}
			require.Empty(t, rec.Header().Get("Content-Disposition"))
		})
	}
}

func TestUploadSourceUnknownLengthOverflowAborts(t *testing.T) {
	maximum := remoteImportSniffBytes + 10
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write(uploadSourcePNG(maximum + 1))
	}), int64(maximum))
	rec := httptest.NewRecorder()
	require.PanicsWithValue(t, http.ErrAbortHandler, func() { service.HandleUploadSource(rec, uploadSourceTestRequest(t)) })
	require.Len(t, rec.Body.Bytes(), maximum)
	// Prove the net/http sentinel makes the consumer receive a read failure.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture := uploadSourceTestRequest(t)
		service.HandleUploadSource(w, r.WithContext(fixture.Context()))
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL + uploadSourceTestRequest(t).URL.RequestURI())
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.ReadAll(response.Body)
	require.Error(t, err)
}

type uploadSourceCancelWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w uploadSourceCancelWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.cancel()
	return n, err
}

func TestUploadSourceCancellationAbortsAndCloses(t *testing.T) {
	closed := make(chan struct{})
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		_, _ = w.Write(uploadSourcePNG(remoteImportSniffBytes))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}), remoteImportSniffBytes*2)
	req := uploadSourceTestRequest(t)
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		service.HandleUploadSource(uploadSourceCancelWriter{httptest.NewRecorder(), cancel}, req.WithContext(ctx))
	})
	select {
	case <-closed:
	case <-t.Context().Done():
		t.Fatal("upstream body not closed")
	}
}

func TestUploadSourceDeclaredLengthMismatchAborts(t *testing.T) {
	original := uploadSourcePNG(remoteImportSniffBytes + 10)
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(original)+1))
		_, _ = w.Write(original)
	}), int64(len(original)+1))
	rec := httptest.NewRecorder()
	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		service.HandleUploadSource(rec, uploadSourceTestRequest(t))
	})
	require.Equal(t, original, rec.Body.Bytes())
}

func TestUploadSourceBelowMinimumRejected(t *testing.T) {
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(uploadSourcePNG(20))
	}), 100)
	service.uploadConfigs[managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE].MinSize = 30
	rec := httptest.NewRecorder()
	service.HandleUploadSource(rec, uploadSourceTestRequest(t))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, rec.Header().Get("Content-Disposition"))
}

func TestUploadSourceUnsafeFilenameUsesCanonicalFallback(t *testing.T) {
	original := uploadSourcePNG(20)
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(original)
	}), 100)
	req := uploadSourceTestRequest(t)
	query := req.URL.Query()
	query.Set("url", "https://media.example.com/evil%0D%0Aheader.jpg")
	req.URL.RawQuery = query.Encode()
	rec := httptest.NewRecorder()
	service.HandleUploadSource(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	_, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	require.NoError(t, err)
	require.Equal(t, "download-source.png", params["filename"])
}

func TestUploadSourceLibraryQueryIdentities(t *testing.T) {
	for _, kind := range []managev1.UploadType{managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE} {
		q := url.Values{"uploadType": {strconv.Itoa(int(kind))}, "url": {"https://media.example.com/file.png"}}
		opts, err := parseUploadSourceQuery(q.Encode())
		require.NoError(t, err)
		require.Empty(t, opts.entityID)
		require.Empty(t, opts.entityType)
		for _, key := range []string{"entityId", "entityType"} {
			q.Set(key, "")
			_, err = parseUploadSourceQuery(q.Encode())
			require.Error(t, err)
			q.Del(key)
		}
	}
}

func TestUploadSourcePublicRedirectPreservesDownload(t *testing.T) {
	original := uploadSourcePNG(20)
	service, _ := uploadSourceTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/final.png" {
			http.Redirect(w, r, "https://cdn.example.com/final.png", http.StatusFound)
			return
		}
		_, _ = w.Write(original)
	}), 100)
	rec := httptest.NewRecorder()
	service.HandleUploadSource(rec, uploadSourceTestRequest(t))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, original, rec.Body.Bytes())
}
