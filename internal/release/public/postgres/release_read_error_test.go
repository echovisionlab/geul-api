package releasepublicpostgres

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Every relation read of a public Release answers the database's failure
// instead of an empty projection.
func TestReleaseRelationReadsReturnDatabaseErrors(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:release-relation-errors-"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	store := NewStore(db, Ports{})
	releaseID := uuid.NewString()
	ctx := context.Background()

	for name, load := range map[string]func() error{
		"artwork":         func() error { _, err := store.ArtworkFiles(ctx, []string{releaseID}); return err },
		"artists":         func() error { _, err := store.ReleaseArtists(ctx, []string{releaseID}); return err },
		"labels":          func() error { _, err := store.ReleaseLabels(ctx, releaseID); return err },
		"genres":          func() error { _, err := store.Genres(ctx, releaseID); return err },
		"styles":          func() error { _, err := store.Styles(ctx, releaseID); return err },
		"formats":         func() error { _, err := store.Formats(ctx, releaseID); return err },
		"release credits": func() error { _, err := store.Credits(ctx, releaseID); return err },
		"credit notes":    func() error { _, err := store.CreditNotes(ctx, releaseID, "en"); return err },
		"tracks":          func() error { _, err := store.Tracks(ctx, releaseID); return err },
		"track files":     func() error { _, err := store.TrackFiles(ctx, []string{uuid.NewString()}); return err },
		"track credits":   func() error { _, err := store.TrackCredits(ctx, []string{uuid.NewString()}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, load())
		})
	}
}
