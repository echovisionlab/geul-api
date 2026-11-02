package favicontest

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type Harness struct {
	mutationtest.Harness[favicon.Store]
	SeedFile    func(*testing.T, string, string)
	SeedAsset   func(*testing.T, string, string, favicon.DerivativeAsset)
	SeedBinding func(*testing.T, string)
}

func ContractAsset(now time.Time) favicon.DerivativeAsset {
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	return favicon.DerivativeAsset{ID: id, Kind: "favicon", ObjectKey: "asset/" + id + ".png", Extension: "png", MimeType: "image/png", FileSize: new(int64(64)), SHA256: digest[:], Disposition: "inline", Status: favicon.AssetReady, CreatedAt: now.UTC().Truncate(time.Microsecond)}
}

func RunContract(t *testing.T, open func(*testing.T) Harness) {
	t.Helper()
	cases := map[string]func(*testing.T, Harness){
		"source and derivative reads preserve stored identities and metadata":           faviconContractSourceMetadata,
		"source lookup ignores deleted unready and unrelated assets":                    faviconContractSourceExclusions,
		"deletion locks unique sorted source and derivative assets and counts bindings": faviconContractDeletionScope,
		"delete pending transition is compare and swap and rolls back":                  faviconContractDeletePendingRollback,
		"asset update lock blocks competing deletion":                                   faviconContractAssetLock,
		"dangling scan excludes cutoff young bound and non favicon assets":              faviconContractDanglingScan,
	}
	for name, run := range stagingCases() {
		cases[name] = run
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}

func faviconContractSourceMetadata(t *testing.T, h Harness) {
	file := uuid.NewString()
	h.SeedFile(t, file, "image/png")
	a, b := ContractAsset(time.Now()), ContractAsset(time.Now())
	h.SeedAsset(t, file, favicon.PNG32.String(), a)
	h.SeedAsset(t, file, favicon.ICO.String(), b)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		mime, found, err := s.SourceMimeType(ctx, file)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "image/png", mime)
		_, found, err = s.SourceMimeType(ctx, uuid.NewString())
		require.NoError(t, err)
		require.False(t, found)
		rows, err := s.FaviconDerivatives(ctx, file)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.Equal(t, []string{favicon.ICO.String(), favicon.PNG32.String()}, []string{rows[0].Type, rows[1].Type})
		require.Equal(t, a.ID, rows[1].ID)
		require.Equal(t, a.SHA256, rows[1].SHA256)
		require.Equal(t, a.FileSize, rows[1].FileSize)
		ref, err := s.AssetRef(rows[1])
		require.NoError(t, err)
		require.Equal(t, a.ID, ref.AssetID)
		require.Equal(t, "https://cdn.example.com/asset/"+a.ID+"/favicon.png", ref.URL)
		require.Equal(t, int64(64), ref.FileSize)
		require.Equal(t, a.SHA256, ref.SHA256)
		count, err := s.FaviconDerivativeCount(ctx, file)
		require.NoError(t, err)
		require.Equal(t, int64(2), count)
		return nil
	}))
}

func faviconContractSourceExclusions(t *testing.T, h Harness) {
	file, other, pending := uuid.NewString(), uuid.NewString(), uuid.NewString()
	h.SeedFile(t, file, "image/svg+xml")
	h.SeedFile(t, other, "image/svg+xml")
	h.SeedFile(t, pending, "image/svg+xml")
	now := time.Now().UTC().Truncate(time.Microsecond)
	older, newer, unready, foreign := ContractAsset(now.Add(-time.Hour)), ContractAsset(now), ContractAsset(now.Add(time.Hour)), ContractAsset(now.Add(2*time.Hour))
	older.SourceFileID, newer.SourceFileID, unready.SourceFileID, foreign.SourceFileID = &file, &file, &pending, &other
	older.Status = favicon.AssetDeleted
	unready.Status = favicon.AssetAllocated
	unready.FileSize, unready.SHA256 = nil, nil
	for _, a := range []favicon.DerivativeAsset{older, newer, unready, foreign} {
		h.SeedAsset(t, "", "", a)
	}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		got, found, err := s.NewestReadySourceAsset(ctx, file)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, newer.ID, got.ID)
		count, err := s.SourceAssetCount(ctx, file)
		require.NoError(t, err)
		require.Equal(t, int64(2), count)
		_, found, err = s.NewestReadySourceAsset(ctx, pending)
		require.NoError(t, err)
		require.False(t, found)
		return nil
	}))
}

func faviconContractDeletionScope(t *testing.T, h Harness) {
	file := uuid.NewString()
	h.SeedFile(t, file, "image/png")
	a, b := ContractAsset(time.Now()), ContractAsset(time.Now())
	a.SourceFileID = &file
	h.SeedAsset(t, file, favicon.PNG32.String(), a)
	h.SeedAsset(t, file, favicon.PNG16.String(), b)
	h.SeedBinding(t, b.ID)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		rows, err := s.LockDeletionAssets(ctx, file)
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.Less(t, rows[0].ID, rows[1].ID)
		count, err := s.BindingCount(ctx, []string{a.ID, b.ID})
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		bound, err := s.AssetBound(ctx, a.ID)
		require.NoError(t, err)
		require.False(t, bound)
		bound, err = s.AssetBound(ctx, b.ID)
		require.NoError(t, err)
		require.True(t, bound)
		return nil
	}))
}

func faviconContractDeletePendingRollback(t *testing.T, h Harness) {
	a := ContractAsset(time.Now())
	h.SeedAsset(t, "", "", a)
	abort := errors.New("owner rollback")
	require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		changed, err := s.MarkAssetDeletePending(ctx, a.ID, time.Now())
		require.NoError(t, err)
		require.True(t, changed)
		changed, err = s.MarkAssetDeletePending(ctx, a.ID, time.Now())
		require.NoError(t, err)
		require.False(t, changed)
		return abort
	}), abort)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		row, found, err := s.LockAsset(ctx, a.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, favicon.AssetReady, row.Status)
		_, found, err = s.LockAsset(ctx, uuid.NewString())
		require.NoError(t, err)
		require.False(t, found)
		return s.RequestAssetDeletion(ctx, a.ID)
	}))
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		row, found, err := s.LockAsset(ctx, a.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, favicon.AssetDeletePending, row.Status)
		return nil
	}))
}

func faviconContractAssetLock(t *testing.T, h Harness) {
	a := ContractAsset(time.Now())
	h.SeedAsset(t, "", "", a)
	require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s favicon.Store) error { _, _, err := s.LockAsset(ctx, a.ID); return err }, func(ctx context.Context, s favicon.Store) error {
		changed, err := s.MarkAssetDeletePending(ctx, a.ID, time.Now())
		require.True(t, changed)
		return err
	}, func(ctx context.Context, s favicon.Store) error {
		changed, err := s.MarkAssetDeletePending(ctx, a.ID, time.Now())
		require.False(t, changed)
		return err
	}))
}

func faviconContractDanglingScan(t *testing.T, h Harness) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-24 * time.Hour)
	old, bound, edge, young, foreign := ContractAsset(cutoff.Add(-time.Hour)), ContractAsset(cutoff.Add(-time.Hour)), ContractAsset(cutoff), ContractAsset(now), ContractAsset(cutoff.Add(-time.Hour))
	foreign.Kind = "image"
	for _, a := range []favicon.DerivativeAsset{old, bound, edge, young, foreign} {
		h.SeedAsset(t, "", "", a)
	}
	h.SeedBinding(t, bound.ID)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s favicon.Store) error {
		ids, err := s.DanglingReadyAssetIDs(ctx, cutoff)
		require.NoError(t, err)
		require.Equal(t, []string{old.ID}, ids)
		return nil
	}))
}
