package artist

import (
	"testing"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/stretchr/testify/require"
)

func TestParseNullableDocumentStringUpdateRequiresExplicitOperation(t *testing.T) {
	_, err := parseNullableDocumentStringUpdate("slug", &intrav1.NullableStringMutation{})
	require.Error(t, err)
	_, err = parseNullableDocumentStringUpdate("slug", &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Set{Set: "  "}})
	require.Error(t, err)
	update, err := parseNullableDocumentStringUpdate("slug", &intrav1.NullableStringMutation{Operation: &intrav1.NullableStringMutation_Clear{Clear: true}})
	require.NoError(t, err)
	require.True(t, update.present)
	require.Nil(t, update.value)
}

func TestBuildArtistDocumentMetadataPlanCanonicalizesLabelIDs(t *testing.T) {
	first := "22222222-2222-4222-8222-222222222222"
	second := "11111111-1111-4111-8111-111111111111"
	plan, err := buildArtistDocumentMetadataPlan(&intrav1.ArtistDocumentMetadataUpdate{LabelIds: &intrav1.ArtistLabelIdsValue{Values: []string{first, second}}})
	require.NoError(t, err)
	require.Equal(t, []string{second, first}, plan.labelIDs)
	_, err = buildArtistDocumentMetadataPlan(&intrav1.ArtistDocumentMetadataUpdate{LabelIds: &intrav1.ArtistLabelIdsValue{Values: []string{first, first}}})
	require.Error(t, err)
}
