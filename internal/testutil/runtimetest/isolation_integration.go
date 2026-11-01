//go:build integration

package runtimetest

import (
	"context"

	pgtest "github.com/echovisionlab/geul-api/internal/testutil/pgtest"
)

// openIsolatedS3Buckets creates media and cache buckets for one test. Buckets
// are never emptied: the MinIO container is discarded with the suite.
func openIsolatedS3Buckets(ctx context.Context, backend *pgtest.AppIntegrationBackendLease) (string, string, error) {
	prefix, err := pgtest.NewIsolationIdentifier("it-")
	if err != nil {
		return "", "", err
	}
	media, cache := prefix+"-media", prefix+"-cache"
	if err := createBackendIntegrationBuckets(ctx, backend.S3Endpoint, media, cache); err != nil {
		return "", "", err
	}
	return media, cache, nil
}
