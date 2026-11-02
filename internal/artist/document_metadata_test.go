package artist

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildArtistDocumentMetadataPlanCanonicalizesLabelIDs(t *testing.T) {
	first := "22222222-2222-4222-8222-222222222222"
	second := "11111111-1111-4111-8111-111111111111"
	plan, err := buildArtistDocumentMetadataPlan(&DocumentMetadataUpdate{LabelIDsPresent: true, LabelIDs: []string{first, second}})
	require.NoError(t, err)
	require.Equal(t, []string{second, first}, plan.labelIDs)
	_, err = buildArtistDocumentMetadataPlan(&DocumentMetadataUpdate{LabelIDsPresent: true, LabelIDs: []string{first, first}})
	require.Error(t, err)
}
