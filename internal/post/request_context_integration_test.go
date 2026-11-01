//go:build integration

package post_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func withPostAuditedRequestContext(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	user := auth.GetUser(ctx)
	require.NotNil(t, user)
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.99")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(ctx, requestContext), user)
}

func postAuditedSystemRequestContext(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}

// postAuditFiles is a File service that resolves no media.
type postAuditFiles struct{}

func (postAuditFiles) DeleteFileByID(context.Context, string) error { return nil }
func (postAuditFiles) ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*commonv1.MediaDelivery, error) {
	return nil, nil
}
func (postAuditFiles) ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error) {
	return map[string]*commonv1.MediaDelivery{}, nil
}
