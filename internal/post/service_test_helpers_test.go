package post

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewService(Deps{CDNDomain: "cdn.example"})
	require.EqualError(t, err, "post dependencies are required: Audit, ContentBlocks, DB, Files, Media, MediaLoader, Members, OG, ShareLinkCleanup, ShareLinks, Signals, SpiceDB, VersionRestore")
}

func TestNewCommentServiceNamesEveryMissingDependency(t *testing.T) {
	_, err := NewCommentService(CommentDeps{})
	require.EqualError(t, err, "comment dependencies are required: Audit, DB, Members, SpiceDB")
}
