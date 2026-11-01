package public_test

import (
	"testing"

	. "github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	t.Parallel()

	_, err := NewService(Deps{})
	require.EqualError(t, err, "public file media dependencies are required: SpiceDB, Units")
}
