package dependencycheck

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMustNotNilRejectsTypedNilValues(t *testing.T) {
	var dependency *int
	require.PanicsWithValue(t, "dependency is required", func() { MustNotNil(dependency, "dependency") })
}

func TestMustNotNilAcceptsConcreteValues(t *testing.T) {
	require.NotPanics(t, func() { MustNotNil("value", "value") })
}
