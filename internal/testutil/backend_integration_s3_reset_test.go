//go:build integration

package testutil

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/require"
)

type fakeBackendIntegrationS3Bucket struct {
	versions      []s3types.ObjectVersion
	deleteMarkers []s3types.DeleteMarkerEntry
	objects       []s3types.Object
	uploads       []s3types.MultipartUpload
}

type fakeBackendIntegrationS3ResetClient struct {
	buckets      map[string]*fakeBackendIntegrationS3Bucket
	deadlineSeen bool
}

func (client *fakeBackendIntegrationS3ResetClient) requireBucket(
	ctx context.Context,
	bucket *string,
) (*fakeBackendIntegrationS3Bucket, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, fmt.Errorf("S3 reset context has no deadline")
	}
	if time.Until(deadline) > backendIntegrationS3ResetTimeout {
		return nil, fmt.Errorf("S3 reset deadline exceeds reset timeout")
	}
	client.deadlineSeen = true
	state, ok := client.buckets[aws.ToString(bucket)]
	if !ok {
		return nil, fmt.Errorf("unknown bucket %s", aws.ToString(bucket))
	}
	return state, nil
}

func (client *fakeBackendIntegrationS3ResetClient) ListMultipartUploads(
	ctx context.Context,
	input *s3.ListMultipartUploadsInput,
	_ ...func(*s3.Options),
) (*s3.ListMultipartUploadsOutput, error) {
	bucket, err := client.requireBucket(ctx, input.Bucket)
	if err != nil {
		return nil, err
	}
	output := &s3.ListMultipartUploadsOutput{}
	if len(bucket.uploads) != 0 {
		output.Uploads = append(output.Uploads, bucket.uploads[0])
	}
	return output, nil
}

func (client *fakeBackendIntegrationS3ResetClient) AbortMultipartUpload(
	ctx context.Context,
	input *s3.AbortMultipartUploadInput,
	_ ...func(*s3.Options),
) (*s3.AbortMultipartUploadOutput, error) {
	bucket, err := client.requireBucket(ctx, input.Bucket)
	if err != nil {
		return nil, err
	}
	for index, upload := range bucket.uploads {
		if aws.ToString(upload.Key) == aws.ToString(input.Key) &&
			aws.ToString(upload.UploadId) == aws.ToString(input.UploadId) {
			bucket.uploads = append(bucket.uploads[:index], bucket.uploads[index+1:]...)
			return &s3.AbortMultipartUploadOutput{}, nil
		}
	}
	return nil, fmt.Errorf("multipart upload not found")
}

func (client *fakeBackendIntegrationS3ResetClient) ListObjectVersions(
	ctx context.Context,
	input *s3.ListObjectVersionsInput,
	_ ...func(*s3.Options),
) (*s3.ListObjectVersionsOutput, error) {
	bucket, err := client.requireBucket(ctx, input.Bucket)
	if err != nil {
		return nil, err
	}
	output := &s3.ListObjectVersionsOutput{}
	if len(bucket.versions) != 0 {
		output.Versions = append(output.Versions, bucket.versions[0])
	} else if len(bucket.deleteMarkers) != 0 {
		output.DeleteMarkers = append(output.DeleteMarkers, bucket.deleteMarkers[0])
	}
	return output, nil
}

func (client *fakeBackendIntegrationS3ResetClient) ListObjectsV2(
	ctx context.Context,
	input *s3.ListObjectsV2Input,
	_ ...func(*s3.Options),
) (*s3.ListObjectsV2Output, error) {
	bucket, err := client.requireBucket(ctx, input.Bucket)
	if err != nil {
		return nil, err
	}
	output := &s3.ListObjectsV2Output{}
	if len(bucket.objects) != 0 {
		output.Contents = append(output.Contents, bucket.objects[0])
	}
	return output, nil
}

func (client *fakeBackendIntegrationS3ResetClient) DeleteObjects(
	ctx context.Context,
	input *s3.DeleteObjectsInput,
	_ ...func(*s3.Options),
) (*s3.DeleteObjectsOutput, error) {
	bucket, err := client.requireBucket(ctx, input.Bucket)
	if err != nil {
		return nil, err
	}
	for _, object := range input.Delete.Objects {
		deleted := false
		if object.VersionId != nil {
			bucket.versions, deleted = deleteFakeBackendIntegrationObjectVersion(
				bucket.versions,
				object,
			)
			if !deleted {
				bucket.deleteMarkers, deleted = deleteFakeBackendIntegrationDeleteMarker(
					bucket.deleteMarkers,
					object,
				)
			}
		} else {
			bucket.objects, deleted = deleteFakeBackendIntegrationCurrentObject(bucket.objects, object)
		}
		if !deleted {
			return &s3.DeleteObjectsOutput{Errors: []s3types.Error{{
				Key:     object.Key,
				Code:    aws.String("NotFound"),
				Message: aws.String("fake object not found"),
			}}}, nil
		}
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func deleteFakeBackendIntegrationObjectVersion(
	versions []s3types.ObjectVersion,
	want s3types.ObjectIdentifier,
) ([]s3types.ObjectVersion, bool) {
	for index, version := range versions {
		if aws.ToString(version.Key) == aws.ToString(want.Key) &&
			aws.ToString(version.VersionId) == aws.ToString(want.VersionId) {
			return append(versions[:index], versions[index+1:]...), true
		}
	}
	return versions, false
}

func deleteFakeBackendIntegrationDeleteMarker(
	markers []s3types.DeleteMarkerEntry,
	want s3types.ObjectIdentifier,
) ([]s3types.DeleteMarkerEntry, bool) {
	for index, marker := range markers {
		if aws.ToString(marker.Key) == aws.ToString(want.Key) &&
			aws.ToString(marker.VersionId) == aws.ToString(want.VersionId) {
			return append(markers[:index], markers[index+1:]...), true
		}
	}
	return markers, false
}

func deleteFakeBackendIntegrationCurrentObject(
	objects []s3types.Object,
	want s3types.ObjectIdentifier,
) ([]s3types.Object, bool) {
	for index, object := range objects {
		if aws.ToString(object.Key) == aws.ToString(want.Key) {
			return append(objects[:index], objects[index+1:]...), true
		}
	}
	return objects, false
}

func TestResetBackendIntegrationS3BucketsPurgesStateAndKeepsBuckets(t *testing.T) {
	client := &fakeBackendIntegrationS3ResetClient{buckets: map[string]*fakeBackendIntegrationS3Bucket{
		"media": {
			versions: []s3types.ObjectVersion{
				{Key: aws.String("versioned.jpg"), VersionId: aws.String("v2")},
				{Key: aws.String("versioned.jpg"), VersionId: aws.String("v1")},
			},
			deleteMarkers: []s3types.DeleteMarkerEntry{
				{Key: aws.String("deleted.jpg"), VersionId: aws.String("marker-1")},
			},
			objects: []s3types.Object{
				{Key: aws.String("current.jpg")},
			},
			uploads: []s3types.MultipartUpload{
				{Key: aws.String("pending.mp4"), UploadId: aws.String("upload-1")},
				{Key: aws.String("pending.wav"), UploadId: aws.String("upload-2")},
			},
		},
		"cache": {
			objects: []s3types.Object{{Key: aws.String("cached.webp")}},
		},
	}}

	require.NoError(t, resetBackendIntegrationS3Buckets(context.Background(), client, "media", "cache"))
	require.True(t, client.deadlineSeen)
	require.Len(t, client.buckets, 2, "reset must preserve the buckets")
	for name, bucket := range client.buckets {
		require.Empty(t, bucket.versions, name)
		require.Empty(t, bucket.deleteMarkers, name)
		require.Empty(t, bucket.objects, name)
		require.Empty(t, bucket.uploads, name)
	}
}

func TestRunBackendIntegrationBoundedCleanupAddsDeadline(t *testing.T) {
	wantErr := errors.New("cleanup failed")
	err := runBackendIntegrationBoundedCleanup(func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), backendIntegrationCleanupTimeout)
		return wantErr
	})
	require.ErrorIs(t, err, wantErr)
}
