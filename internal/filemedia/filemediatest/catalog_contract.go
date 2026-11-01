package filemediatest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type CatalogHarness struct {
	SeedScoped       func(*testing.T, string, string, ScopedOwner, domain.ScopedAttachment)
	SeedPolicy       func(*testing.T, domain.PolicyRelation, []string)
	SeedSegment      func(*testing.T, domain.PolicySegment)
	Audit            domainaudit.Appender
	SeedAssetBinding func(*testing.T, string, string)
	Asset            func(*testing.T, string) mediaasset.Asset
	Outbox           func(*testing.T) []domain.DeletionTarget
	AuditCount       func(*testing.T) int
	Mesh             func(*testing.T, string) (MeshRecord, bool)
	mutationtest.Harness[domain.Store]
	SeedAsset         func(*testing.T, mediaasset.Asset)
	SeedGeneration    func(*testing.T, mediaasset.Generation)
	SeedDerivative    func(*testing.T, domain.StoredDerivativeDelivery)
	SeedFile          func(*testing.T, domain.CatalogRow, bool, bool)
	SeedAvatar        func(*testing.T, string, string)
	SeedMesh          func(*testing.T, domain.MeshDeliveryWitness, string)
	SeedDeliveryOwner func(*testing.T, string, string, string, string, string)
}

func RunCatalogContract(t *testing.T, open func(*testing.T) CatalogHarness) {
	t.Helper()
	cases := map[string]func(*testing.T, CatalogHarness){
		"delivery read projections retain asset generation and mesh facts": func(t *testing.T, h CatalogHarness) {
			row, output, pending := CatalogFile("original", nil), CatalogFile("output", nil), CatalogFile("pending", nil)
			h.SeedFile(t, row, false, false)
			h.SeedFile(t, output, false, false)
			h.SeedFile(t, pending, true, false)
			now := time.Now().UTC()
			assetID, generationID := uuid.NewString(), uuid.NewString()
			size := int64(123)
			count := int32(2)
			source := mediaasset.Asset{ID: assetID, SourceFileID: &row.ID, Kind: "image", ObjectKey: "asset/" + assetID + ".webp", Extension: "webp", MimeType: "image/webp", FileSize: &size, SHA256: bytes.Repeat([]byte{7}, 32), Disposition: "inline", Status: mediaasset.AssetReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now}
			h.SeedAsset(t, source)
			h.SeedGeneration(t, mediaasset.Generation{ID: generationID, FileID: row.ID, Kind: "hls", ObjectPrefix: "media/" + row.ID + "/hls/" + generationID, ManifestName: "master.m3u8", ManifestSHA256: bytes.Repeat([]byte{8}, 32), ObjectCount: &count, TotalSize: &size, Status: mediaasset.GenerationReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now})
			asset := domain.StoredDerivativeDelivery{FileID: row.ID, Type: "FILE_DERIVATIVE_TYPE_THUMBNAIL", AssetID: &assetID, AssetKind: new("image"), AssetExtension: new("webp"), AssetMimeType: new("image/webp"), AssetFileSize: &size, AssetSHA256: source.SHA256, AssetDisposition: new("inline"), AssetStatus: new("ready")}
			generation := domain.StoredDerivativeDelivery{FileID: row.ID, Type: "FILE_DERIVATIVE_TYPE_HLS", MediaGenerationID: &generationID, MediaGenerationFileID: &row.ID, MediaGenerationManifest: new("master.m3u8"), MediaGenerationStatus: new("ready")}
			h.SeedDerivative(t, asset)
			h.SeedDerivative(t, generation)
			witness := domain.MeshDeliveryWitness{CandidateID: uuid.NewString(), SourceFileID: row.ID, OutputFileID: output.ID}
			h.SeedMesh(t, witness, "ready")
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				for _, full := range []bool{false, true} {
					rows, err := s.StoredFileDeliveries(ctx, []string{row.ID, pending.ID, uuid.NewString()}, full)
					require.NoError(t, err)
					require.Len(t, rows, 1)
					got := rows[0]
					require.Equal(t, row.ID, got.ID)
					require.Equal(t, &row.Name, got.FileName)
					require.Equal(t, *row.Extension, got.Extension)
					require.Equal(t, *row.MimeType, got.MimeType)
					require.Equal(t, *row.FileSize, got.FileSize)
					require.Equal(t, int(*row.DurationSeconds), *got.DurationSeconds)
				}
				derivatives, err := s.StoredDerivativeDeliveries(ctx, []string{row.ID})
				require.NoError(t, err)
				require.ElementsMatch(t, []domain.StoredDerivativeDelivery{asset, generation}, derivatives)
				refs, err := s.SourceAssetReferences(ctx, "https://cdn.example.com", "image", []string{row.ID, output.ID})
				require.NoError(t, err)
				require.Len(t, refs, 1)
				require.Equal(t, assetID, refs[row.ID].AssetID)
				require.Equal(t, "https://cdn.example.com/asset/"+assetID+"/image.webp", refs[row.ID].URL)
				require.Equal(t, source.SHA256, refs[row.ID].SHA256)
				mesh, err := s.MeshDeliveryOutputs(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, []domain.MeshDeliveryOutput{{ID: witness.CandidateID, Status: "ready", OutputFileID: &output.ID}}, mesh)
				failures, err := s.DeliveryProcessingFailures(ctx, []string{row.ID}, []string{output.ID})
				require.NoError(t, err)
				require.Empty(t, failures.Audio)
				require.Empty(t, failures.Video)
				require.Empty(t, failures.Waveform)
				return nil
			}))
		},

		"delivery authorization facts include pending and exact current owner": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("authorization", nil)
			h.SeedFile(t, row, true, false)
			owner, doc, avatar := uuid.NewString(), uuid.NewString(), uuid.NewString()
			h.SeedAvatar(t, row.ID, avatar)
			h.SeedDeliveryOwner(t, "post", owner, doc, "POST_STATUS_PUBLISHED", row.ID)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				files, err := s.DeliveryFileOwnership(ctx, []string{row.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Len(t, files, 1)
				require.Equal(t, row.ID, files[0].ID)
				require.NotNil(t, files[0].DeleteRequestedAt)
				require.Empty(t, files[0].FileName)
				bindings, err := s.DeliveryIngestBindings(ctx, []string{row.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Len(t, bindings, 1)
				require.Equal(t, avatar, bindings[0].EntityID)
				require.Equal(t, "UPLOAD_TYPE_USER_AVATAR", bindings[0].UploadType)
				usages, err := s.CurrentDeliveryUsages(ctx, []string{row.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Equal(t, []domain.CurrentDeliveryUsage{{Kind: "featured_image", FileID: row.ID, ResourceType: "post", ResourceID: owner, RelationID: owner, ReferencePath: "featured_image", OwnerFingerprint: doc + ":POST_STATUS_PUBLISHED"}}, usages)
				usages, err = s.CurrentDeliveryUsages(ctx, nil)
				require.Empty(t, usages)
				return err
			}))
		},

		"delivery projections preserve each locking scope": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "parent")
			row := CatalogFile("delivery", &parent.ID)
			h.SeedFile(t, row, true, false)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				for _, scope := range []domain.DeliveryFileScope{domain.CatalogDelivery, domain.StrongDelivery} {
					files, err := s.LockDeliveryFiles(ctx, []string{row.ID, uuid.NewString()}, scope)
					require.NoError(t, err)
					require.Len(t, files, 1)
					got := files[row.ID]
					require.Equal(t, row.Name, got.FileName)
					require.Equal(t, *row.Extension, got.Extension)
					require.Equal(t, *row.MimeType, got.MimeType)
					require.Equal(t, *row.FileSize, got.FileSize)
					require.Equal(t, int(*row.DurationSeconds), *got.DurationSeconds)
					require.NotNil(t, got.DeleteRequestedAt)
					if scope == domain.CatalogDelivery {
						require.Equal(t, row.ParentID, got.FolderID)
						require.True(t, row.CreatedAt.Equal(got.CreatedAt))
						require.True(t, row.UpdatedAt.Equal(got.UpdatedAt))
					} else {
						require.Nil(t, got.FolderID)
						require.True(t, got.CreatedAt.IsZero())
						require.True(t, got.UpdatedAt.IsZero())
					}
				}
				file, found, err := s.LockUsageDeliveryFile(ctx, row.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, row.Name, file.FileName)
				require.Nil(t, file.UploadedByMemberID)
				require.Nil(t, file.FolderID)
				require.True(t, file.CreatedAt.IsZero())
				_, found, err = s.LockUsageDeliveryFile(ctx, uuid.NewString())
				require.NoError(t, err)
				require.False(t, found)
				_, err = s.LockDeliveryFiles(ctx, []string{row.ID}, domain.DeliveryFileScope(99))
				require.Error(t, err)
				return nil
			}))
		},
		"delivery share lock blocks rename": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("before", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.LockDeliveryFiles(ctx, []string{row.ID}, domain.CatalogDelivery)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error { return s.RenameFile(ctx, row.ID, "after") }))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.FileCatalog(ctx, row.ID)
				require.Equal(t, "after", got.Name)
				return err
			}))
		},
		"usage share lock blocks move": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "parent")
			row := CatalogFile("before", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, _, err := s.LockUsageDeliveryFile(ctx, row.ID)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error { return s.MoveFile(ctx, row.ID, &parent.ID) }))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.FileCatalog(ctx, row.ID)
				require.Equal(t, &parent.ID, got.ParentID)
				return err
			}))
		},
		"avatar binding requires exact identity": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("avatar", nil)
			h.SeedFile(t, row, false, false)
			identity := uuid.NewString()
			h.SeedAvatar(t, row.ID, identity)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				ok, err := s.LockAvatarFileBinding(ctx, row.ID, identity)
				require.NoError(t, err)
				require.True(t, ok)
				ok, err = s.LockAvatarFileBinding(ctx, row.ID, uuid.NewString())
				require.NoError(t, err)
				require.False(t, ok)
				ok, err = s.LockAvatarFileBinding(ctx, uuid.NewString(), identity)
				require.False(t, ok)
				return err
			}))
		},
		"mesh witness requires source output and ready state": func(t *testing.T, h CatalogHarness) {
			source, output := CatalogFile("source", nil), CatalogFile("output", nil)
			h.SeedFile(t, source, false, false)
			h.SeedFile(t, output, false, false)
			witness := domain.MeshDeliveryWitness{CandidateID: uuid.NewString(), SourceFileID: source.ID, OutputFileID: output.ID}
			h.SeedMesh(t, witness, "ready")
			pending := witness
			pending.CandidateID = uuid.NewString()
			h.SeedMesh(t, pending, "pending")
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				changed := witness
				changed.SourceFileID = uuid.NewString()
				rows, err := s.LockMeshDeliveryWitnesses(ctx, []domain.MeshDeliveryWitness{changed, pending})
				require.NoError(t, err)
				require.Empty(t, rows)
				rows, err = s.LockMeshDeliveryWitnesses(ctx, []domain.MeshDeliveryWitness{witness})
				require.Equal(t, map[string]bool{witness.CandidateID: true}, rows)
				return err
			}))
		},
		"owner projection and exact featured relation": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("featured", nil)
			h.SeedFile(t, row, false, false)
			owner, document := uuid.NewString(), uuid.NewString()
			h.SeedDeliveryOwner(t, "post", owner, document, "POST_STATUS_PUBLISHED", row.ID)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, ok, err := s.LockDeliveryOwner(ctx, "post", owner)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, owner, got.ID)
				require.Equal(t, &document, got.ContentDocumentID)
				require.Equal(t, "POST_STATUS_PUBLISHED", got.Status)
				w := domain.DeliveryUsageWitness{Kind: "featured_image", ResourceType: "post", ResourceID: owner, FileID: row.ID}
				ok, err = s.LockExactDeliveryUsage(ctx, w)
				require.NoError(t, err)
				require.True(t, ok)
				w.FileID = uuid.NewString()
				ok, err = s.LockExactDeliveryUsage(ctx, w)
				require.NoError(t, err)
				require.False(t, ok)
				_, _, err = s.LockDeliveryOwner(ctx, "unsupported", owner)
				require.Error(t, err)
				_, err = s.LockExactDeliveryUsage(ctx, domain.DeliveryUsageWitness{Kind: "unsupported"})
				require.Error(t, err)
				return nil
			}))
		},

		"rename lock projection and missing sentinel": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("original", nil)
			h.SeedFile(t, row, true, false)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				file, err := s.LockFileForRename(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, row.ID, file.ID)
				require.Equal(t, row.Name, file.FileName)
				require.Equal(t, *row.Extension, file.Extension)
				require.NotNil(t, file.DeleteRequestedAt)
				require.Empty(t, file.MimeType)
				require.Zero(t, file.FileSize)
				_, err = s.LockFileForRename(ctx, uuid.NewString())
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				return nil
			}))
		},
		"move lock projection and presence include pending files": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "parent")
			a, b := CatalogFile("a", &parent.ID), CatalogFile("b", nil)
			a.ID = "11111111-1111-4111-8111-111111111111"
			b.ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
			h.SeedFile(t, a, false, false)
			h.SeedFile(t, b, true, false)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				rows, err := s.LockFilesForMove(ctx, []string{b.ID, a.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Len(t, rows, 2)
				require.Equal(t, a.ID, rows[0].ID)
				require.Equal(t, a.ParentID, rows[0].FolderID)
				require.Empty(t, rows[0].FileName)
				require.NotNil(t, rows[1].DeleteRequestedAt)
				count, err := s.FileCount(ctx, []string{b.ID, a.ID, a.ID, uuid.NewString()})
				require.Equal(t, int64(2), count)
				return err
			}))
		},
		"rename and move preserve file metadata and rollback": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "parent")
			row := CatalogFile("original", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.RenameFile(ctx, row.ID, "renamed"))
				return s.MoveFile(ctx, row.ID, &parent.ID)
			}))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.FileCatalog(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, "renamed", got.Name)
				require.Equal(t, &parent.ID, got.ParentID)
				require.Equal(t, row.Extension, got.Extension)
				require.Equal(t, row.FileSize, got.FileSize)
				return nil
			}))
			abort := errors.New("abort")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				require.NoError(t, s.RenameFile(ctx, row.ID, "rollback"))
				require.NoError(t, s.MoveFile(ctx, row.ID, nil))
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.FileCatalog(ctx, row.ID)
				require.Equal(t, "renamed", got.Name)
				require.Equal(t, &parent.ID, got.ParentID)
				return err
			}))
		},
		"rename row lock blocks a second writer": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("before", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.LockFileForRename(ctx, row.ID)
				return err
			}, func(ctx context.Context, s domain.Store) error { return s.RenameFile(ctx, row.ID, "after") }, func(ctx context.Context, s domain.Store) error {
				got, err := s.LockFileForRename(ctx, row.ID)
				require.Equal(t, "after", got.FileName)
				return err
			}))
		},
		"move row lock blocks a second writer": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "parent")
			row := CatalogFile("file", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.LockFilesForMove(ctx, []string{row.ID})
				return err
			}, func(ctx context.Context, s domain.Store) error { return s.MoveFile(ctx, row.ID, &parent.ID) }, func(ctx context.Context, s domain.Store) error {
				got, err := s.LockFilesForMove(ctx, []string{row.ID})
				require.Len(t, got, 1)
				require.Equal(t, &parent.ID, got[0].FolderID)
				return err
			}))
		},

		"directory scope and folder priority": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "z-folder")
			insertFolder(t, h.Harness, &parent.ID, "child")
			root, child := CatalogFile("a-root", nil), CatalogFile("a-child", &parent.ID)
			h.SeedFile(t, root, false, false)
			h.SeedFile(t, child, false, false)
			rows := readCatalog(t, h, domain.CatalogQuery{IncludeFolders: true, PageSize: 50})
			require.Equal(t, []string{parent.ID, root.ID}, catalogIDs(rows))
			require.Equal(t, int64(2), rows[0].Total)
			rows = readCatalog(t, h, domain.CatalogQuery{Parent: parent.ID, IncludeFolders: true, PageSize: 50})
			require.Len(t, rows, 2)
			require.Equal(t, "folder", rows[0].ItemType)
			require.Equal(t, child.ID, rows[1].ID)
			for _, row := range rows {
				require.JSONEq(t, "[]", string(row.FolderPath))
			}
		},
		"pending and generated output filters differ from direct lookup": func(t *testing.T, h CatalogHarness) {
			ready, pending, output := CatalogFile("ready", nil), CatalogFile("pending", nil), CatalogFile("generated", nil)
			h.SeedFile(t, ready, false, false)
			h.SeedFile(t, pending, true, false)
			h.SeedFile(t, output, false, true)
			rows := readCatalog(t, h, domain.CatalogQuery{PageSize: 50})
			require.Equal(t, []string{ready.ID}, catalogIDs(rows))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				row, err := s.FileCatalog(ctx, output.ID)
				require.NoError(t, err)
				require.Equal(t, output.ID, row.ID)
				row, err = s.FileCatalog(ctx, pending.ID)
				require.NoError(t, err)
				require.Empty(t, row.ID)
				return nil
			}))
		},
		"file projections and ordered ID lookup": func(t *testing.T, h CatalogHarness) {
			folder := insertFolder(t, h.Harness, nil, "folder")
			a, b := CatalogFile("first", &folder.ID), CatalogFile("second", nil)
			a.ID = "11111111-1111-4111-8111-111111111111"
			b.ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
			h.SeedFile(t, a, false, false)
			h.SeedFile(t, b, false, false)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.FileCatalog(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, a.ID, got.ID)
				require.Equal(t, a.Name, got.Name)
				require.Equal(t, a.ParentID, got.ParentID)
				require.Equal(t, a.Extension, got.Extension)
				require.Equal(t, a.MimeType, got.MimeType)
				require.Equal(t, a.FileSize, got.FileSize)
				require.Equal(t, a.DurationSeconds, got.DurationSeconds)
				require.True(t, a.CreatedAt.Equal(got.CreatedAt))
				require.True(t, a.UpdatedAt.Equal(got.UpdatedAt))
				require.Equal(t, int64(1), got.Total)
				require.Nil(t, got.FolderPath)
				rows, err := s.FileCatalogs(ctx, []string{b.ID, a.ID, b.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Equal(t, []string{a.ID, b.ID}, catalogIDs(rows))
				missing, err := s.FileCatalog(ctx, uuid.NewString())
				require.NoError(t, err)
				require.Empty(t, missing.ID)
				return nil
			}))
		},
		"pagination keeps total and stable ties": func(t *testing.T, h CatalogHarness) {
			for _, id := range []string{"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "11111111-1111-4111-8111-111111111111"} {
				row := CatalogFile("same", nil)
				row.ID = id
				h.SeedFile(t, row, false, false)
			}
			q := domain.CatalogQuery{PageSize: 1}
			first := readCatalog(t, h, q)
			require.Equal(t, "11111111-1111-4111-8111-111111111111", first[0].ID)
			require.Equal(t, int64(2), first[0].Total)
			q.Offset = 1
			second := readCatalog(t, h, q)
			require.Equal(t, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", second[0].ID)
			require.Equal(t, int64(2), second[0].Total)
			q.Offset = 2
			require.Empty(t, readCatalog(t, h, q))
		},
		"all sort fields preserve ascending ID ties": func(t *testing.T, h CatalogHarness) {
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"}
			for i, name := range []string{"alpha", "Bravo", "charlie", "ALPHA"} {
				row := CatalogFile(name, nil)
				row.ID = ids[i]
				created := []int{1, 2, 3, 1}[i]
				updated := []int{4, 3, 2, 4}[i]
				size := []int64{3, 1, 2, 3}[i]
				row.CreatedAt = at.Add(time.Duration(created) * time.Hour)
				row.UpdatedAt = at.Add(time.Duration(updated) * time.Hour)
				row.FileSize = &size
				h.SeedFile(t, row, false, false)
			}
			for _, tc := range []struct {
				field domain.CatalogSort
				desc  bool
				order []int
			}{{domain.CatalogSortName, false, []int{0, 3, 1, 2}}, {domain.CatalogSortName, true, []int{2, 1, 0, 3}}, {domain.CatalogSortCreated, false, []int{0, 3, 1, 2}}, {domain.CatalogSortCreated, true, []int{2, 1, 0, 3}}, {domain.CatalogSortUpdated, false, []int{2, 1, 0, 3}}, {domain.CatalogSortUpdated, true, []int{0, 3, 1, 2}}, {domain.CatalogSortSize, false, []int{1, 2, 0, 3}}, {domain.CatalogSortSize, true, []int{0, 3, 2, 1}}} {
				rows := readCatalog(t, h, domain.CatalogQuery{PageSize: 50, SortField: tc.field, Descending: tc.desc})
				want := []string{}
				for _, i := range tc.order {
					want = append(want, ids[i])
				}
				require.Equal(t, want, catalogIDs(rows))
			}
		},
		"search scopes descendants and projects full folder path": func(t *testing.T, h CatalogHarness) {
			root := insertFolder(t, h.Harness, nil, "needle-root")
			child := insertFolder(t, h.Harness, &root.ID, "needle-child")
			other := insertFolder(t, h.Harness, nil, "other")
			a, b, c := CatalogFile("needle-root-file", &root.ID), CatalogFile("needle-child-file", &child.ID), CatalogFile("needle-outside", &other.ID)
			for _, row := range []domain.CatalogRow{a, b, c} {
				h.SeedFile(t, row, false, false)
			}
			rows := readCatalog(t, h, domain.CatalogQuery{Parent: root.ID, Query: "needle", IncludeFolders: true, PageSize: 50})
			require.ElementsMatch(t, []string{child.ID, a.ID, b.ID}, catalogIDs(rows))
			for _, row := range rows {
				require.Equal(t, int64(3), row.Total)
				var path []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				require.NoError(t, json.Unmarshal(row.FolderPath, &path))
				require.Equal(t, root.ID, path[0].ID)
				if row.ID == a.ID {
					require.Len(t, path, 1)
				} else {
					require.Len(t, path, 2)
					require.Equal(t, child.ID, path[1].ID)
					require.Equal(t, "needle-child", path[1].Name)
				}
			}
		},
		"search includes extension and MIME filters": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, false, false)
			rows := readCatalog(t, h, domain.CatalogQuery{Query: ".WEBP", MimePrefix: "IMAGE/", PageSize: 50})
			require.Equal(t, []string{row.ID}, catalogIDs(rows))
			require.Empty(t, readCatalog(t, h, domain.CatalogQuery{MimePrefix: "audio/", PageSize: 50}))
			require.Empty(t, readCatalog(t, h, domain.CatalogQuery{Uploader: uuid.NewString(), PageSize: 50}))
		},
	}
	for name, run := range promotionContractCases() {
		cases[name] = run
	}
	for name, run := range ingestReceiptCases() {
		cases[name] = run
	}
	for name, run := range reuseContractCases() {
		cases[name] = run
	}
	for name, run := range scopedContractCases() {
		cases[name] = run
	}
	for name, run := range policyContractCases() {
		cases[name] = run
	}
	for name, run := range libraryDeletionCases() {
		cases[name] = run
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
func CatalogFile(name string, parent *string) domain.CatalogRow {
	ext, mime, size, duration := "webp", "image/webp", int64(1024), int32(4)
	at := time.Now().UTC().Truncate(time.Microsecond)
	return domain.CatalogRow{ItemType: "file", ID: uuid.NewString(), ParentID: parent, Name: name, Extension: &ext, MimeType: &mime, FileSize: &size, DurationSeconds: &duration, CreatedAt: at, UpdatedAt: at}
}
func readCatalog(t *testing.T, h CatalogHarness, q domain.CatalogQuery) []domain.CatalogRow {
	t.Helper()
	var rows []domain.CatalogRow
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		var err error
		rows, err = s.Catalog(ctx, q)
		return err
	}))
	return rows
}
func catalogIDs(rows []domain.CatalogRow) []string {
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}
