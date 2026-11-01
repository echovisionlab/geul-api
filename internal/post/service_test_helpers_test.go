package post

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{})
	require.EqualError(t, err, "post dependencies are required: Audit, Fence, Files, Members, SpiceDB, Units")
}

func TestNewCommentServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewCommentService(CommentDeps{})
	require.EqualError(t, err, "comment dependencies are required: Audit, Fence, Members, SpiceDB, Units")
}
