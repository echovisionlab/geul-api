package referencecatalog

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

func TestNormalizeReferenceNameAndSlug(t *testing.T) {
	t.Parallel()

	name, err := normalizeReferenceName("  Ambient  ")
	require.NoError(t, err)
	require.Equal(t, "Ambient", name)

	slug, err := normalizeReferenceSlug("  ambient-drone  ")
	require.NoError(t, err)
	require.Equal(t, "ambient-drone", slug)

	_, err = normalizeReferenceName("   ")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = normalizeReferenceSlug("   ")
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = normalizeReferenceName(strings.Repeat("가", referenceValueMaxLength+1))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = normalizeReferenceSlug(strings.Repeat("가", referenceValueMaxLength+1))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
