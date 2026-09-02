//go:build integration

package emailauthoring

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestEmailLayoutPreviewContentValidation(t *testing.T) {
	t.Parallel()

	svc := &EmailLayoutService{
		cdnDomain:  "https://cdn.example.com",
		siteOrigin: "https://example.com",
	}
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc.spiceDB = stack.SpiceDBClient
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())

	validPreview, err := svc.PreviewEmailLayoutContent(ctx, connect.NewRequest(&managev1.PreviewEmailLayoutContentRequest{
		HtmlContent:   "<html><body><h1>{{subject}}</h1><main>{{content}}</main></body></html>",
		SampleContent: new("<p>Live preview body</p>"),
	}))
	require.NoError(t, err)
	require.True(t, validPreview.Msg.Valid)
	require.Empty(t, validPreview.Msg.Errors)
	require.Contains(t, validPreview.Msg.Html, "Live preview body")
	require.Contains(t, validPreview.Msg.Html, "Sample Email Subject")

	tests := []struct {
		name      string
		content   string
		wantCodes []string
	}{
		{
			name:      "empty content",
			content:   "   ",
			wantCodes: []string{"EMPTY_CONTENT"},
		},
		{
			name:      "missing content placeholder",
			content:   "<html><body><main>No content slot</main></body></html>",
			wantCodes: []string{"MISSING_CONTENT_PLACEHOLDER"},
		},
		{
			name:      "duplicated document",
			content:   "<!DOCTYPE html><html><body><main>{{content}}</main></body></html><!DOCTYPE html><html><body><main>{{content}}</main></body></html>",
			wantCodes: []string{"MULTIPLE_CONTENT_PLACEHOLDERS", "MULTIPLE_HTML_DOCUMENTS"},
		},
		{
			name:      "unclosed tag",
			content:   "<html><body><div>{{content}}</body></html>",
			wantCodes: []string{"UNCLOSED_TAG"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preview, err := svc.PreviewEmailLayoutContent(ctx, connect.NewRequest(&managev1.PreviewEmailLayoutContentRequest{
				HtmlContent: tt.content,
			}))
			require.NoError(t, err)
			require.False(t, preview.Msg.Valid)
			require.Empty(t, preview.Msg.Html)
			require.Subset(t, emailLayoutValidationErrorCodes(preview.Msg.Errors), tt.wantCodes)
		})
	}
}

func emailLayoutValidationErrorCodes(errors []*managev1.EmailLayoutValidationError) []string {
	codes := make([]string, 0, len(errors))
	for _, err := range errors {
		codes = append(codes, err.Code)
	}
	return codes
}
