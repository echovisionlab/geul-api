package filemediatest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func libraryDeletionCases() map[string]func(*testing.T, CatalogHarness) {
	return map[string]func(*testing.T, CatalogHarness){
		"deletion projection and guarded intent rollback": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("delete", nil)
			h.SeedFile(t, row, false, false)
			now := time.Now().UTC()
			abort := errors.New("abort")
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				f, err := s.LockDeletionCandidate(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, row.ID, f.ID)
				require.Nil(t, f.DeleteRequestedAt)
				require.Empty(t, f.Extension)
				_, err = s.LockDeletionCandidate(ctx, uuid.NewString())
				require.ErrorIs(t, err, domain.ErrFileNotFound)
				changed, err := s.MarkFileDeletion(ctx, row.ID, now)
				require.NoError(t, err)
				require.True(t, changed)
				changed, err = s.MarkFileDeletion(ctx, row.ID, now)
				require.NoError(t, err)
				require.False(t, changed)
				f, err = s.DeletionEventFile(ctx, row.ID)
				require.NoError(t, err)
				require.Equal(t, *row.Extension, f.Extension)
				require.Equal(t, *row.MimeType, f.MimeType)
				require.True(t, f.DeleteRequestedAt.Equal(now))
				return abort
			}), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				n, err := s.ActiveFileCount(ctx, row.ID)
				require.Equal(t, int64(1), n)
				return err
			}))
		},
		"deletion update lock blocks a second mark": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("delete", nil)
			h.SeedFile(t, row, false, false)
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.LockDeletionCandidate(ctx, row.ID)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error {
				ok, err := s.MarkFileDeletion(ctx, row.ID, time.Now().UTC())
				require.True(t, ok)
				return err
			}))
		},
		"reference preflight excludes provenance and includes exact product and mesh refs": func(t *testing.T, h CatalogHarness) {
			source, output, avatar := CatalogFile("source", nil), CatalogFile("output", nil), CatalogFile("avatar", nil)
			for _, r := range []domain.CatalogRow{source, output, avatar} {
				h.SeedFile(t, r, false, false)
			}
			h.SeedAvatar(t, avatar.ID, uuid.NewString())
			h.SeedDeliveryOwner(t, "post", uuid.NewString(), uuid.NewString(), "POST_STATUS_PUBLISHED", source.ID)
			w := domain.MeshDeliveryWitness{CandidateID: uuid.NewString(), SourceFileID: source.ID, OutputFileID: output.ID}
			h.SeedMesh(t, w, "ready")
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				refs, err := s.ActiveFileReferenceIDs(ctx, []string{source.ID, output.ID, avatar.ID})
				require.NoError(t, err)
				require.Equal(t, map[string]struct{}{source.ID: {}, output.ID: {}}, refs)
				names, err := s.ActiveFileReferenceNames(ctx, source.ID)
				require.NoError(t, err)
				require.Equal(t, []string{"post.featured_image_file_id"}, names)
				names, err = s.ActiveFileReferenceNames(ctx, avatar.ID)
				require.NoError(t, err)
				require.Empty(t, names)
				rows, err := s.FileUsages(ctx, []string{source.ID})
				require.NoError(t, err)
				require.Len(t, rows, 1)
				require.Equal(t, "post", rows[0].Domain)
				require.Equal(t, "featured_image", rows[0].ReferencePath)
				return nil
			}))
		},
		"folder subtree deletion detaches pending and active files and rolls back": func(t *testing.T, h CatalogHarness) {
			root := insertFolder(t, h.Harness, nil, "root")
			child := insertFolder(t, h.Harness, &root.ID, "child")
			other := insertFolder(t, h.Harness, nil, "other")
			a, b, c := CatalogFile("active", &child.ID), CatalogFile("pending", &child.ID), CatalogFile("other", &other.ID)
			h.SeedFile(t, a, false, false)
			h.SeedFile(t, b, true, false)
			h.SeedFile(t, c, false, false)
			abort := errors.New("abort")
			act := func(ctx context.Context, s domain.Store) error {
				ids, err := s.LockFolderSubtree(ctx, root.ID)
				require.NoError(t, err)
				require.ElementsMatch(t, []string{root.ID, child.ID}, ids)
				files, err := s.FolderDeletionFileIDs(ctx, ids)
				require.NoError(t, err)
				require.Equal(t, []string{a.ID}, files)
				require.NoError(t, s.DetachDeletionFiles(ctx, files))
				return s.DeleteFolder(ctx, root.ID)
			}
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { require.NoError(t, act(ctx, s)); return abort }), abort)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				f, err := s.FileCatalog(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, &child.ID, f.ParentID)
				return nil
			}))
			require.NoError(t, mutationtest.Transact(t, h.Harness, act))
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				rows, err := s.LockFilesForMove(ctx, []string{a.ID, b.ID, c.ID})
				require.NoError(t, err)
				require.Len(t, rows, 3)
				byID := map[string]domain.File{}
				for _, row := range rows {
					byID[row.ID] = row
				}
				require.Contains(t, byID, a.ID)
				require.Contains(t, byID, b.ID)
				require.Contains(t, byID, c.ID)
				require.Nil(t, byID[a.ID].FolderID)
				require.Nil(t, byID[b.ID].FolderID)
				require.NotNil(t, byID[b.ID].DeleteRequestedAt)
				require.Equal(t, &other.ID, byID[c.ID].FolderID)
				return nil
			}))
		},
		"folder subtree lock blocks descendant rename": func(t *testing.T, h CatalogHarness) {
			root := insertFolder(t, h.Harness, nil, "root")
			child := insertFolder(t, h.Harness, &root.ID, "child")
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				_, err := s.LockFolderSubtree(ctx, root.ID)
				return err
			}, func(context.Context, domain.Store) error { return nil }, func(ctx context.Context, s domain.Store) error { return s.RenameFolder(ctx, child.ID, "renamed") }))
		},
		"deletion outbox audit and intent share commit and rollback": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("atomic", nil)
			h.SeedFile(t, row, false, false)
			now := time.Now().UTC()
			abort := errors.New("abort")
			act := func(ctx context.Context, s domain.Store) error {
				_, err := s.MarkFileDeletion(ctx, row.ID, now)
				require.NoError(t, err)
				target, err := domain.BuildFileDeletionTarget(ctx, s, row.ID)
				require.NoError(t, err)
				require.Equal(t, "media/"+row.ID+"."+*row.Extension, target.Original.ObjectKey)
				require.NoError(t, s.DeletionAudit(h.Audit).AppendDomainAuditInTransaction(ctx, nil, telemetry.AuditRecord{}))
				return s.EnqueueFileDeletion(ctx, *target)
			}
			require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error { require.NoError(t, act(ctx, s)); return abort }), abort)
			require.Empty(t, h.Outbox(t))
			require.Zero(t, h.AuditCount(t))
			require.NoError(t, mutationtest.Transact(t, h.Harness, act))
			events := h.Outbox(t)
			require.Len(t, events, 1)
			require.Equal(t, row.ID, events[0].FileID)
			require.Equal(t, 1, h.AuditCount(t))
		},
		"asset deletion preserves bindings and excludes favicon derivatives": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("assets", nil)
			h.SeedFile(t, row, false, false)
			now := time.Now().UTC()
			size := int64(32)
			mk := func(kind, ext, mime string) mediaasset.Asset {
				id := uuid.NewString()
				return mediaasset.Asset{ID: id, SourceFileID: &row.ID, Kind: kind, ObjectKey: "asset/" + id + "." + ext, Extension: ext, MimeType: mime, FileSize: &size, SHA256: make([]byte, 32), Disposition: "inline", Status: mediaasset.AssetReady, ReadyAt: &now, CreatedAt: now, UpdatedAt: now}
			}
			unbound, bound, icon := mk("thumbnail", "webp", "image/webp"), mk("waveform", "json", "application/json"), mk("favicon", "png", "image/png")
			for _, a := range []mediaasset.Asset{unbound, bound, icon} {
				h.SeedAsset(t, a)
			}
			h.SeedAssetBinding(t, bound.ID, row.ID)
			for _, r := range []domain.StoredDerivativeDelivery{{FileID: row.ID, Type: "FILE_DERIVATIVE_TYPE_THUMBNAIL", AssetID: &unbound.ID}, {FileID: row.ID, Type: "FILE_DERIVATIVE_TYPE_WAVEFORM", AssetID: &bound.ID}, {FileID: row.ID, Type: "FILE_DERIVATIVE_TYPE_FAVICON_PNG_16", AssetID: &icon.ID}} {
				h.SeedDerivative(t, r)
			}
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return domain.RequestUnboundDerivativeAssetsDeletion(ctx, s, row.ID)
			}))
			require.Equal(t, mediaasset.AssetDeletePending, h.Asset(t, unbound.ID).Status)
			require.Equal(t, mediaasset.AssetReady, h.Asset(t, bound.ID).Status)
			require.Equal(t, mediaasset.AssetReady, h.Asset(t, icon.ID).Status)
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				return domain.RequestUnboundSourceAssetsDeletion(ctx, s, row.ID)
			}))
			require.Equal(t, mediaasset.AssetDeletePending, h.Asset(t, icon.ID).Status)
			require.Equal(t, mediaasset.AssetReady, h.Asset(t, bound.ID).Status)

		},
		"mesh deletion cancels inflight and removes terminal candidates": func(t *testing.T, h CatalogHarness) {
			source, out := CatalogFile("source", nil), CatalogFile("output", nil)
			h.SeedFile(t, source, false, false)
			h.SeedFile(t, out, false, false)
			pending, ready := domain.MeshDeliveryWitness{CandidateID: uuid.NewString(), SourceFileID: source.ID, OutputFileID: out.ID}, domain.MeshDeliveryWitness{CandidateID: uuid.NewString(), SourceFileID: source.ID, OutputFileID: out.ID}
			h.SeedMesh(t, pending, "processing")
			h.SeedMesh(t, ready, "ready")
			now := time.Now().UTC()
			require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				ids, err := domain.PrepareMeshCandidatesForDeletion(ctx, s, source.ID, now)
				require.NoError(t, err)
				require.Equal(t, []string{out.ID}, ids)
				return nil
			}))
			m, found := h.Mesh(t, pending.CandidateID)
			require.True(t, found)
			require.Equal(t, "cancelled", m.Status)
			require.True(t, m.CancelledAt.Equal(now))
			require.True(t, m.ExpiresAt.Equal(now.Add(7*24*time.Hour)))
			_, found = h.Mesh(t, ready.CandidateID)
			require.False(t, found)
		},
	}
}
