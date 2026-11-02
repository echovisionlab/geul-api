//go:build integration

package post_test

import (
	"context"
	"testing"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

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
func (postAuditFiles) ResolveAuthorizedPostFeaturedImage(context.Context, string, string) (*fileref.Delivery, error) {
	return nil, nil
}
func (postAuditFiles) ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error) {
	return map[string]*fileref.Delivery{}, nil
}
