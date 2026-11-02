package emaildelivery

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "email delivery dependencies are required: AdapterCache, AdapterChanges, Audit, Fence, Providers, SpiceDB, Units")
}
