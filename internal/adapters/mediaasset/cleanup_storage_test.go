package mediaasset

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestCleanupStorageDeletePrefixReportsPartialFailureAndRetriesRemainingObjects(t *testing.T) {
	t.Parallel()
	const prefix = "media/source/hls/generation/"
	keys := []string{prefix + "master.m3u8", prefix + "segment.ts"}
	failedKey := keys[1]
	var storageMu sync.Mutex
	storedKeys := map[string]bool{keys[0]: true, keys[1]: true}
	deleteBatches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		storageMu.Lock()
		defer storageMu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2" && request.URL.Query().Get("prefix") == prefix:
			var contents strings.Builder
			for _, key := range keys {
				if storedKeys[key] {
					fmt.Fprintf(&contents, "<Contents><Key>%s</Key><Size>1</Size></Contents>", key)
				}
			}
			fmt.Fprintf(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>media</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><IsTruncated>false</IsTruncated>%s</ListBucketResult>`, prefix, len(storedKeys), contents.String())
		case request.Method == http.MethodPost && request.URL.Query().Has("delete"):
			var batch struct {
				Objects []struct{ Key string } `xml:"Object"`
				Quiet   bool                   `xml:"Quiet"`
			}
			if err := xml.NewDecoder(request.Body).Decode(&batch); err != nil || !batch.Quiet {
				http.Error(w, "invalid deletion request", http.StatusBadRequest)
				return
			}
			deleteBatches++
			for _, object := range batch.Objects {
				if deleteBatches == 1 && object.Key == failedKey {
					continue
				}
				delete(storedKeys, object.Key)
			}
			if deleteBatches == 1 {
				fmt.Fprintf(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Error><Key>%s</Key><Code>AccessDenied</Code><Message>injected per-key failure</Message></Error></DeleteResult>`, failedKey)
			} else {
				fmt.Fprint(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
			}
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	client := s3.NewFromConfig(aws.Config{
		Region: "us-east-1", Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("test", "test", "")),
		HTTPClient: server.Client(), Retryer: func() aws.Retryer { return aws.NopRetryer{} },
	}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	storage := NewCleanupStorage(client, "media")
	err := storage.DeletePrefix(t.Context(), prefix)
	require.ErrorContains(t, err, "AccessDenied")
	require.ErrorContains(t, err, failedKey)
	storageMu.Lock()
	remaining := len(storedKeys)
	failedKeyRemains := storedKeys[failedKey]
	storageMu.Unlock()
	require.Equal(t, 1, remaining)
	require.True(t, failedKeyRemains)

	require.NoError(t, storage.DeletePrefix(t.Context(), prefix))
	storageMu.Lock()
	remaining = len(storedKeys)
	batches := deleteBatches
	storageMu.Unlock()
	require.Zero(t, remaining)
	require.Equal(t, 2, batches)
}
