package work

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "work dependencies are required: Audit, Fence, Members, SpiceDB, Units")
}
