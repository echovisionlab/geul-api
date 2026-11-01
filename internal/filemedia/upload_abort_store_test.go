package filemedia_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type abortedUploadStore struct {
	*filemediatest.LibraryStore
	deleteFailure error
}

func (s *abortedUploadStore) DeleteAbortedUpload(ctx context.Context, id string) (bool, error) {
	if s.deleteFailure != nil {
		return false, s.deleteFailure
	}
	return s.LibraryStore.DeleteAbortedUpload(ctx, id)
}

func TestAbortMultipartUploadPreservesSessionWhenDeletionFails(t *testing.T) {
	s := &abortedUploadStore{LibraryStore: filemediatest.NewLibraryStore(), deleteFailure: errors.New("delete failed")}
	service, ctx, request, row, calls := abortFixture(t, s, domain.UploadSessionStatusUploading)
	response, err := filemedia.NewConnectHandler(service).AbortMultipartUpload(ctx, request)
	require.Error(t, err)
	require.Nil(t, response)
	require.Equal(t, connect.CodeInternal, errs.CodeOf(err))
	require.Equal(t, row.UploadID, <-calls)
	require.Equal(t, domain.UploadSessionStatusAborted, s.UploadSessions[row.UploadID].Status)
}

func TestAbortAndRejectedPartCannotReplaceFinalizingAuthority(t *testing.T) {
	for _, path := range []string{"RPC", "part rejection"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			s := &abortedUploadStore{LibraryStore: filemediatest.NewLibraryStore()}
			service, ctx, request, row, calls := abortFixture(t, s, domain.UploadSessionStatusFinalizing)
			if path == "RPC" {
				response, err := filemedia.NewConnectHandler(service).AbortMultipartUpload(ctx, request)
				require.Error(t, err)
				require.Nil(t, response)
				require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
			} else {
				r := httptest.NewRequest(http.MethodPut, "/upload-part", nil).WithContext(ctx)
				w := httptest.NewRecorder()
				require.False(t, filemedia.ExportRejectUploadPart(service, w, r, row))
				require.Equal(t, http.StatusConflict, w.Code)
			}
			select {
			case id := <-calls:
				t.Fatalf("aborted finalizing upload %s", id)
			default:
			}
			require.Equal(t, domain.UploadSessionStatusFinalizing, s.UploadSessions[row.UploadID].Status)
		})
	}
}

func abortFixture(t *testing.T, store *abortedUploadStore, status domain.UploadSessionStatus) (*filemedia.Service, context.Context, *connect.Request[managev1.AbortMultipartUploadRequest], domain.UploadSession, <-chan string) {
	t.Helper()
	user := &auth.UserInfo{IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()), SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true}
	row := domain.UploadSession{UploadID: uuid.NewString(), FileID: uuid.NewString(), UploadType: "UPLOAD_TYPE_USER_AVATAR", EntityID: user.MemberID.String(), FileName: "avatar.jpg", FileSize: 1, RequestedMime: "image/jpeg", TotalParts: 1, ChunkSize: 1, Status: status}
	store.UploadSessions[row.UploadID] = row
	calls := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.URL.Query().Get("uploadId")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	storage := s3.New(s3.Options{BaseEndpoint: aws.String(server.URL), Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), UsePathStyle: true, HTTPClient: server.Client()})
	service := filemedia.ExportAbortService(mutationtest.New[filemedia.Store](store), storage, authztest.AllowAll().Client(t))
	ctx := auth.WithUser(t.Context(), user)
	return service, ctx, connect.NewRequest(&managev1.AbortMultipartUploadRequest{UploadId: row.UploadID, FileId: row.FileID}), row, calls
}
