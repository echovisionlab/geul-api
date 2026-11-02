package filemedia

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
)

func TestClassifyFileMediaGenerationDeleteRefsIncludesRetiredGenerations(t *testing.T) {
	t.Parallel()
	fileID := uuid.NewString()
	currentID := uuid.NewString()
	retiredID := uuid.NewString()
	currentPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, currentID)
	require.NoError(t, err)
	retiredPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, retiredID)
	require.NoError(t, err)

	generations, err := domain.ClassifyGenerationDeleteRefs(fileID, []domain.FileGenerationDeleteRef{
		{ID: currentID, FileID: fileID, Kind: "hls", ObjectPrefix: currentPrefix},
		{ID: retiredID, FileID: fileID, Kind: "hls", ObjectPrefix: retiredPrefix},
	})
	require.NoError(t, err)
	require.Len(t, generations, 2)
	require.Equal(t, []string{currentID, retiredID}, []string{
		generations[0].GenerationID, generations[1].GenerationID,
	})
	require.Equal(t, []string{currentPrefix, retiredPrefix}, []string{
		generations[0].ObjectPrefix, generations[1].ObjectPrefix,
	})
}
