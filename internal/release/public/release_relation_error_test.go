package public

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
)

func TestReleaseRelationLoadersReturnDatabaseErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:release-relation-errors-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	service := &ReleaseService{db: db, spiceDB: &auth.SpiceDBClient{}}
	releaseID := uuid.NewString()

	tests := []struct {
		name string
		load func() error
	}{
		{
			name: "artwork",
			load: func() error {
				_, loadErr := service.loadReleaseArtworkAssets(context.Background(), []string{releaseID})
				return loadErr
			},
		},
		{
			name: "artists",
			load: func() error {
				_, loadErr := service.loadReleaseArtists(context.Background(), []string{releaseID})
				return loadErr
			},
		},
		{
			name: "labels",
			load: func() error {
				_, loadErr := service.getReleaseLabels(context.Background(), releaseID)
				return loadErr
			},
		},
		{
			name: "genres",
			load: func() error {
				_, loadErr := service.getReleaseGenres(context.Background(), releaseID)
				return loadErr
			},
		},
		{
			name: "styles",
			load: func() error {
				_, loadErr := service.getReleaseStyles(context.Background(), releaseID)
				return loadErr
			},
		},
		{
			name: "formats",
			load: func() error {
				_, loadErr := service.getReleaseFormats(context.Background(), releaseID)
				return loadErr
			},
		},
		{
			name: "release credits",
			load: func() error {
				_, loadErr := service.getReleaseCredits(context.Background(), releaseID, "en", "en")
				return loadErr
			},
		},
		{
			name: "tracks",
			load: func() error {
				_, loadErr := service.getReleaseTracks(context.Background(), releaseID, ReleaseStatusPublished, mediaasset.ContentDownloadOwnerAuthorization{
					ResourceType: "release", ResourceID: releaseID, Status: ReleaseStatusPublished,
					Mode: mediaasset.ContentDownloadOwnerAccessPublic,
				})
				return loadErr
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, test.load())
		})
	}
}
