package menu

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "menu dependencies are required: Audit, Fence, SpiceDB, Units")
}
