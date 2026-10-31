package public

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestBuildPostResponseRejectsInvalidStoredDocumentLayout(t *testing.T) {
	t.Parallel()
	service := &Service{}
	require.NotPanics(t, func() {
		response, err := service.buildPostResponse(context.Background(), "", &publicPostSnapshot{
			post: model.Post{ID: "post", DocumentLayout: model.DocumentLayout{ContentHeight: "bogus"}},
		})
		require.Error(t, err)
		require.Nil(t, response)
	})
}
