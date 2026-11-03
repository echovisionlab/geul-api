package criteria

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "criteria dependencies are required: Audit, Fence, RecipientCounter, SpiceDB, Units")
}
