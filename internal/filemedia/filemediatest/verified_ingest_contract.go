package filemediatest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func ingestContractFile() domain.File {
	return domain.File{ID: uuid.NewString(), FileName: "verified", Extension: "webp", MimeType: "image/webp", FileSize: 64, SHA256: bytes.Repeat([]byte{5}, 32), IngestSlotID: new(uuid.NewString()), IngestAttemptID: new(uuid.NewString())}
}

func verifiedIngestCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"upload owner facts preserve missing and unsupported targets": func(t *testing.T, h CatalogHarness) {
			file, owner, doc := CatalogFile("owner", nil), uuid.NewString(), uuid.NewString()
			h.SeedFile(t, file, false, false)
			h.SeedDeliveryOwner(t, "post", owner, doc, "POST_STATUS_DRAFT", file.ID)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				exists, err := s.UploadOwnerExists(ctx, "post", owner)
				require.NoError(t, err)
				require.True(t, exists)
				exists, err = s.UploadOwnerExists(ctx, "post", uuid.NewString())
				require.NoError(t, err)
				require.False(t, exists)
				exists, err = s.UploadOwnerExists(ctx, "user", owner)
				require.NoError(t, err)
				require.False(t, exists)
				return nil
			}))
		},
		"upload Work existence uses owner errors": func(t *testing.T, h CatalogHarness) {
			id, doc := uuid.NewString(), uuid.NewString()
			file := CatalogFile("work", nil)
			h.SeedFile(t, file, false, false)
			h.SeedScoped(t, "work", id, ScopedOwner{DocumentID: doc}, domain.ScopedAttachment{BlockID: uuid.NewString(), ReferencePath: "file", FileID: file.ID})
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.RequireUploadWork(ctx, id))
				var missing *domainerr.Error
				require.ErrorAs(t, s.RequireUploadWork(ctx, uuid.NewString()), &missing)
				require.Equal(t, domainerr.NotFound, missing.Kind)
				return nil
			}))
		},
		"concurrent ingest date folder allocation converges": func(t *testing.T, h CatalogHarness) {
			var first *string
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				var err error
				first, err = s.EnsureIngestDateFolder(ctx)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				second, err := s.EnsureIngestDateFolder(ctx)
				require.Equal(t, first, second)
				return err
			}))
		},
		"date folder ingest preserves uploader checksum and operation identity": func(t *testing.T, h CatalogHarness) {
			prior := CatalogFile("prior", nil)
			prior.MemberID = new(uuid.NewString())
			h.SeedFile(t, prior, false, false)
			file := ingestContractFile()
			file.UploadedByMemberID = prior.MemberID
			target := uuid.NewString()
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				var err error
				file.FolderID, err = s.EnsureIngestDateFolder(ctx)
				require.NoError(t, err)
				require.NotNil(t, file.FolderID)
				again, err := s.EnsureIngestDateFolder(ctx)
				require.NoError(t, err)
				require.Equal(t, file.FolderID, again)
				day, err := s.Folder(ctx, *file.FolderID)
				require.NoError(t, err)
				require.Len(t, day.Name, 2)
				month, err := s.Folder(ctx, *day.ParentID)
				require.NoError(t, err)
				require.Len(t, month.Name, 2)
				year, err := s.Folder(ctx, *month.ParentID)
				require.NoError(t, err)
				require.Len(t, year.Name, 4)
				require.Nil(t, year.ParentID)
				require.NoError(t, s.InsertIngestFile(ctx, file))
				return s.InsertIngestBinding(ctx, domain.IngestBinding{FileID: file.ID, UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityType: new("TRANSCODE_ENTITY_TYPE_TRACK"), EntityID: target}, false)
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.IngestFile(ctx, file.ID)
				require.NoError(t, err)
				require.Equal(t, file.FileName, got.FileName)
				require.Equal(t, file.SHA256, got.SHA256)
				require.Equal(t, file.FolderID, got.FolderID)
				require.Equal(t, file.UploadedByMemberID, got.UploadedByMemberID)
				require.Equal(t, file.IngestSlotID, got.IngestSlotID)
				require.Equal(t, file.IngestAttemptID, got.IngestAttemptID)
				binding, err := s.IngestBinding(ctx, file.ID)
				require.NoError(t, err)
				require.Equal(t, target, binding.EntityID)
				require.Equal(t, new("TRANSCODE_ENTITY_TYPE_TRACK"), binding.EntityType)
				return nil
			}))
		},
		"independent ingest omits target and optional metadata": func(t *testing.T, h CatalogHarness) {
			file := ingestContractFile()
			file.SHA256, file.IngestSlotID, file.IngestAttemptID = nil, nil, nil
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.InsertIngestFile(ctx, file))
				return s.InsertIngestBinding(ctx, domain.IngestBinding{FileID: file.ID, UploadType: "UPLOAD_TYPE_GENERAL_FILE", EntityID: uuid.NewString()}, true)
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.IngestFile(ctx, file.ID)
				require.NoError(t, err)
				require.Empty(t, got.SHA256)
				require.Nil(t, got.IngestSlotID)
				require.Nil(t, got.IngestAttemptID)
				require.Nil(t, got.UploadedByMemberID)
				binding, err := s.IngestBinding(ctx, file.ID)
				require.NoError(t, err)
				require.Empty(t, binding.EntityID)
				require.Nil(t, binding.EntityType)
				return nil
			}))
		},
		"verified ingest rolls back folder File and binding together": func(t *testing.T, h CatalogHarness) {
			file := ingestContractFile()
			abort := errors.New("owner rollback")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				var err error
				file.FolderID, err = s.EnsureIngestDateFolder(ctx)
				require.NoError(t, err)
				require.NoError(t, s.InsertIngestFile(ctx, file))
				require.NoError(t, s.InsertIngestBinding(ctx, domain.IngestBinding{FileID: file.ID, UploadType: "UPLOAD_TYPE_GENERAL_FILE"}, true))
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.IngestFile(ctx, file.ID)
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				_, err = s.IngestBinding(ctx, file.ID)
				require.ErrorIs(t, err, domain.ErrIngestBindingNotFound)
				_, err = s.Folder(ctx, *file.FolderID)
				require.ErrorIs(t, err, domain.ErrFolderNotFound)
				return nil
			}))
		},
		"invalid ingest checksum rolls back date folder": func(t *testing.T, h CatalogHarness) {
			file := ingestContractFile()
			file.SHA256 = []byte{1}
			require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				var err error
				file.FolderID, err = s.EnsureIngestDateFolder(ctx)
				require.NoError(t, err)
				return s.InsertIngestFile(ctx, file)
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.IngestFile(ctx, file.ID)
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				_, err = s.Folder(ctx, *file.FolderID)
				require.ErrorIs(t, err, domain.ErrFolderNotFound)
				return nil
			}))
		},
		"ingest owner lock serializes the owning row": func(t *testing.T, h CatalogHarness) {
			file, owner, doc := CatalogFile("owner", nil), uuid.NewString(), uuid.NewString()
			h.SeedFile(t, file, false, false)
			h.SeedDeliveryOwner(t, "post", owner, doc, "POST_STATUS_DRAFT", file.ID)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return s.LockIngestOwner(ctx, "post", owner)
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error { return s.LockIngestOwner(ctx, "post", owner) }))
		},
	}
}
