package referencecatalog

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeReferenceNameAndSlug(t *testing.T) {
	t.Parallel()

	name, err := normalizeReferenceName("  Ambient  ", referenceValueMaxLength)
	require.NoError(t, err)
	require.Equal(t, "Ambient", name)

	slug, err := normalizeReferenceSlug("  ambient-drone  ", referenceValueMaxLength)
	require.NoError(t, err)
	require.Equal(t, "ambient-drone", slug)

	_, err = normalizeReferenceName("   ", referenceValueMaxLength)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	_, err = normalizeReferenceSlug("   ", referenceValueMaxLength)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = normalizeReferenceName(strings.Repeat("가", referenceValueMaxLength+1), referenceValueMaxLength)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	_, err = normalizeReferenceSlug(strings.Repeat("가", referenceValueMaxLength+1), referenceValueMaxLength)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = normalizeReferenceName(strings.Repeat("가", shortReferenceValueMaxLength), shortReferenceValueMaxLength)
	require.NoError(t, err)
	_, err = normalizeReferenceName(strings.Repeat("가", shortReferenceValueMaxLength+1), shortReferenceValueMaxLength)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	_, err = normalizeReferenceSlug(strings.Repeat("가", shortReferenceValueMaxLength+1), shortReferenceValueMaxLength)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}
