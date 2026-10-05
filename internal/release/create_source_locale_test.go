package release

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestReleaseProtoPreservesSourceLocale(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		t.Run(locale, func(t *testing.T) {
			root := &model.Release{ID: "11111111-1111-4111-8111-111111111111", SourceLocale: locale}
			response, err := (&ReleaseService{}).releaseResponseWithArtworkOg(root, nil)
			require.NoError(t, err)
			require.Equal(t, locale, response.Msg.SourceLocale, "Create/Get/List share the native Release projection")
		})
	}
}

func TestCreateReleaseSourceLocaleRejectsInvalidExplicitLocale(t *testing.T) {
	for _, locale := range []string{"EN", "en-US", " ko ", "unsupported", " "} {
		t.Run(locale, func(t *testing.T) {
			service := &ReleaseService{contentBlocks: &contentblock.Store{}}
			response, err := service.CreateRelease(t.Context(), connect.NewRequest(&managev1.CreateReleaseRequest{Title: "Requested locale validation", SourceLocale: locale, Type: managev1.ReleaseType_RELEASE_TYPE_ALBUM}))
			require.Nil(t, response)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
			require.Contains(t, err.Error(), "source_locale")
		})
	}
}
