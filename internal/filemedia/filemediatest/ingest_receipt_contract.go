package filemediatest

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func ingestReceiptCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"ingest receipt retains pending deletion and stored metadata": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, true, false)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.IngestFile(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, row.ID, got.ID)
				require.Equal(t, row.Name, got.FileName)
				require.Equal(t, *row.Extension, got.Extension)
				require.Equal(t, *row.FileSize, got.FileSize)
				require.Equal(t, *row.MimeType, got.MimeType)
				require.Equal(t, int(*row.DurationSeconds), *got.DurationSeconds)
				require.NotNil(t, got.DeleteRequestedAt)
				digest := sha256.Sum256([]byte(row.ID))
				require.Equal(t, digest[:], got.SHA256)
				got.SHA256[0] ^= 255
				again, err := s.IngestFile(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, digest[:], again.SHA256)
				count, err := s.FileCount(ctx, []string{row.ID})
				require.NoError(t, err)
				require.Equal(t, int64(1), count)
				_, err = s.IngestFile(ctx, uuid.NewString())
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				return nil
			}))
		},
		"ingest binding retains exact target and missing sentinel": func(t *testing.T, h CatalogHarness) {
			row, owner := CatalogFile("avatar", nil), uuid.NewString()
			h.SeedFile(t, row, false, false)
			h.SeedAvatar(t, row.ID, owner)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.IngestBinding(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, row.ID, got.FileID)
				require.Equal(t, "UPLOAD_TYPE_USER_AVATAR", got.UploadType)
				require.Equal(t, owner, got.EntityID)
				require.Nil(t, got.EntityType)
				_, err = s.IngestBinding(ctx, uuid.NewString())
				require.ErrorIs(t, err, domain.ErrIngestBindingNotFound)
				return nil
			}))
		},
	}
}
