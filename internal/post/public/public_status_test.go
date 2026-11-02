package public

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/post"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestPublicPostStatusBoundary(t *testing.T) {
	for _, test := range []struct {
		name   string
		status managev1.PostStatus
		public bool
	}{
		{name: "draft", status: managev1.PostStatus_POST_STATUS_DRAFT},
		{name: "published", status: managev1.PostStatus_POST_STATUS_PUBLISHED, public: true},
		{name: "scheduled", status: managev1.PostStatus_POST_STATUS_SCHEDULED},
		{name: "archived", status: managev1.PostStatus_POST_STATUS_ARCHIVED, public: true},
		{name: "unspecified", status: managev1.PostStatus_POST_STATUS_UNSPECIFIED},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.public, isPublicPostStatus(post.Status(test.status.String())))
		})
	}
}
