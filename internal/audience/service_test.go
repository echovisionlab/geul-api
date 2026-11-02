package audience

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceRequiresEveryCollaborator(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "audience dependencies are required: Audit, Fence, RecipientCounter, SpiceDB, Units")
}
