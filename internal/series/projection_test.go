package series

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSeriesProjectionsPreserveIndependentSourceLocales(t *testing.T) {
	series := toProtoSeriesSimple(&model.Series{SourceLocale: "ko"})
	post := toProtoSeriesPost(&seriesPostRow{SourceLocale: "pt-BR"})
	require.Equal(t, "ko", series.SourceLocale)
	require.Equal(t, "pt-BR", post.SourceLocale)
}
