package public

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "public Post dependencies are required: Files, Localizer, Members, SpiceDB, Units")
}
