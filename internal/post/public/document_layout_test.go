package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/stretchr/testify/require"
)

func TestBuildPostViewRejectsInvalidStoredDocumentLayout(t *testing.T) {
	t.Parallel()
	service := &Service{}
	require.NotPanics(t, func() {
		_, err := service.buildPostView(t.Context(), "", &publicPostSnapshot{
			post: PublicPost{Post: post.Post{ID: "post", DocumentLayout: post.DocumentLayout{ContentHeight: "bogus"}}},
		})
		require.EqualError(t, err, `invalid persisted document layout: invalid contentHeight "bogus"`)
	})
}
