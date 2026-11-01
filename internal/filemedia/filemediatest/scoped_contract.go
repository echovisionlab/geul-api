package filemediatest

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func scopedContractCases() map[string]func(*testing.T, CatalogHarness) {
	cases := map[string]func(*testing.T, CatalogHarness){}
	for _, kind := range []string{"page", "post", "work", "program_event"} {
		cases["scoped "+kind+" attachments require exact owner document block and file"] = func(t *testing.T, h CatalogHarness) {
			row, pending := CatalogFile("file", nil), CatalogFile("pending", nil)
			h.SeedFile(t, row, false, false)
			h.SeedFile(t, pending, true, false)
			id, doc, block := uuid.NewString(), uuid.NewString(), uuid.NewString()
			attachment := domain.ScopedAttachment{BlockID: block, ReferencePath: "file", FileID: row.ID}
			h.SeedScoped(t, kind, id, ScopedOwner{FeaturedFileID: &row.ID, DocumentID: doc}, attachment)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				files, err := s.LockScopedFiles(ctx, []string{row.ID, pending.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Len(t, files, 2)
				require.Equal(t, "file", files[row.ID].FileName)
				require.NotNil(t, files[pending.ID].DeleteRequestedAt)
				for _, query := range []struct {
					owner, doc, block string
					match             bool
				}{{id, doc, block, true}, {uuid.NewString(), doc, block, false}, {id, uuid.NewString(), block, false}, {id, doc, uuid.NewString(), false}} {
					rows, err := s.LockScopedAttachments(ctx, kind, query.owner, query.doc, []string{query.block})
					require.NoError(t, err)
					if query.match {
						require.Equal(t, map[string]domain.ScopedAttachment{attachment.Key(): attachment}, rows)
					} else {
						require.Empty(t, rows)
					}
				}
				if kind == "page" || kind == "post" {
					fileID, found, err := s.FeaturedFileID(ctx, kind, id)
					require.NoError(t, err)
					require.True(t, found)
					require.Equal(t, &row.ID, fileID)
					_, found, err = s.FeaturedFileID(ctx, kind, uuid.NewString())
					require.NoError(t, err)
					require.False(t, found)
				}
				return nil
			}))
		}
	}
	cases["scoped File lock holds through issuance"] = func(t *testing.T, h CatalogHarness) {
		row := CatalogFile("original", nil)
		h.SeedFile(t, row, false, false)
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
			_, err := s.LockScopedFiles(ctx, []string{row.ID})
			return err
		}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
			_, err := s.LockFileForRename(ctx, row.ID)
			return err
		}))
	}

	return cases
}
