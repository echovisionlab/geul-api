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

func reuseContractCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"reuse facts follow the persisted document and uploader": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("original", nil)
			row.MemberID = new(uuid.NewString())
			h.SeedFile(t, row, false, false)
			owner, doc, block := uuid.NewString(), uuid.NewString(), uuid.NewString()
			h.SeedScoped(t, "page", owner, ScopedOwner{DocumentID: doc}, domain.ScopedAttachment{BlockID: block, ReferencePath: "file", FileID: row.ID})
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				for _, q := range []struct {
					doc, file string
					want      bool
				}{{doc, row.ID, true}, {uuid.NewString(), row.ID, false}, {doc, uuid.NewString(), false}} {
					attached, err := s.FileAlreadyAttached(ctx, q.doc, q.file)
					require.NoError(t, err)
					require.Equal(t, q.want, attached)
				}
				uploader, err := s.FileUploader(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, row.MemberID, uploader)
				uploader, err = s.FileUploader(ctx, uuid.NewString())
				require.NoError(t, err)
				require.Nil(t, uploader)
				return nil
			}))
		},
		"verified metadata requires a live File and retains checksum": func(t *testing.T, h CatalogHarness) {
			row, pending := CatalogFile("original", nil), CatalogFile("pending", nil)
			h.SeedFile(t, row, false, false)
			h.SeedFile(t, pending, true, false)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				f, err := s.VerifiedFileMetadata(ctx, row.ID)
				require.NoError(t, err)
				digest := sha256.Sum256([]byte(row.ID))
				require.Equal(t, domain.File{ID: row.ID, Extension: *row.Extension, MimeType: *row.MimeType, FileSize: *row.FileSize, SHA256: digest[:]}, f)
				for _, id := range []string{pending.ID, uuid.NewString()} {
					_, err := s.VerifiedFileMetadata(ctx, id)
					require.ErrorIs(t, err, domain.ErrFileNotFound)
				}
				return nil
			}))
		},
	}
}
