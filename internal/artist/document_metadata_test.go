package artist

import (
	"testing"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
)

func TestBuildArtistDocumentMetadataPlanCanonicalizesLabelIDs(t *testing.T) {
	first := "22222222-2222-4222-8222-222222222222"
	second := "11111111-1111-4111-8111-111111111111"
	plan, err := buildArtistDocumentMetadataPlan(&intrav1.ArtistDocumentMetadataUpdate{LabelIds: &intrav1.ArtistLabelIdsValue{Values: []string{first, second}}})
	require.NoError(t, err)
	require.Equal(t, []string{second, first}, plan.labelIDs)
	_, err = buildArtistDocumentMetadataPlan(&intrav1.ArtistDocumentMetadataUpdate{LabelIds: &intrav1.ArtistLabelIdsValue{Values: []string{first, first}}})
	require.Error(t, err)
}
