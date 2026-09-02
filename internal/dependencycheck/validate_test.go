package dependencycheck

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatorRejectsTypedNilValues(t *testing.T) {
	var dependency *int
	require.PanicsWithValue(t, "example: dependency is required", func() {
		New("example").RequireNotNil(dependency, "dependency").Validate()
	})
}

func TestMustNotNilAcceptsConcreteValues(t *testing.T) {
	require.NotPanics(t, func() { MustNotNil("value", "value") })
}
