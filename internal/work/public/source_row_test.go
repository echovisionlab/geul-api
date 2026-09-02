package public

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	workdomain "github.com/echovisionlab/geul-api/internal/work"
)

func TestWorkFilterConfigSearchUsesSourceLocaleTitle(t *testing.T) {
	t.Parallel()

	field, ok := workFilterConfig.Fields["search"]
	require.True(t, ok)
	assert.Equal(t, []string{workdomain.WorkSourceTitleSQL("work")}, field.SearchColumns)
}
