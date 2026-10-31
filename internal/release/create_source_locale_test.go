package release

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
)

func TestReleaseProtoPreservesSourceLocale(t *testing.T) {
	for _, locale := range []string{"en", "ko", "ja"} {
		t.Run(locale, func(t *testing.T) {
			root := &model.Release{ID: "11111111-1111-4111-8111-111111111111", SourceLocale: locale}
			require.Equal(t, locale, toProtoRelease(root, nil, nil).SourceLocale, "Create/Get/List share the native Release projection")
		})
	}
}

func TestCreateReleaseSourceLocaleRejectsInvalidExplicitLocale(t *testing.T) {
	service := newTestService(t, Deps{})
	for _, locale := range []string{"EN", "en-US", " ko ", "unsupported", " "} {
		t.Run(locale, func(t *testing.T) {
			created, err := service.CreateRelease(t.Context(), CreateReleaseCommand{Title: "Requested locale validation", SourceLocale: locale, Type: model.ReleaseTypeAlbum})
			require.Nil(t, created)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
			require.Contains(t, err.Error(), "source_locale")
		})
	}
}
