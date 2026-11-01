package artist

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestManageArtistProjectionPreservesSourceLocaleOnWire(t *testing.T) {
	for _, locale := range []string{"ko", "en", "pt-BR", "zh-TW"} {
		t.Run(locale, func(t *testing.T) {
			response := artistMessage(View{Artist: Artist{SourceLocale: locale}})
			wire, err := protojson.Marshal(response)
			require.NoError(t, err)
			decoded := new(managev1.Artist)
			require.NoError(t, protojson.Unmarshal(wire, decoded))
			require.Equal(t, locale, decoded.SourceLocale)
		})
	}
}
