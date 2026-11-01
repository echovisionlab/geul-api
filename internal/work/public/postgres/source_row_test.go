package workpublicpostgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	workpostgres "github.com/echovisionlab/geul-api/internal/work/postgres"
)

func TestWorkFilterConfigSearchUsesSourceLocaleTitle(t *testing.T) {
	t.Parallel()

	field, ok := workFilterConfig.Fields["search"]
	require.True(t, ok)
	assert.Equal(t, []string{workpostgres.WorkSourceTitleSQL("work")}, field.SearchColumns)
}
