package publictest

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/public"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type Harness struct {
	mutationtest.Harness[public.Store]
	SeedFile       func(*testing.T, FileRecord)
	SeedAsset      func(*testing.T, AssetRecord)
	SeedGeneration func(*testing.T, GenerationRecord)
	SeedDerivative func(*testing.T, public.Derivative)
	SeedOwner      func(*testing.T, public.OwnerWitness)
	SeedRelation   func(*testing.T, Relation)
	SeedTrack      func(*testing.T, Track)
	SeedShareLink  func(*testing.T, public.ShareLink)
	UpdateOwner    func(context.Context, public.Store, public.OwnerWitness) error
}

func RunStoreContract(t *testing.T, open func(*testing.T) Harness) {
	t.Helper()
	cases := map[string]func(*testing.T, Harness){
		"ready asset metadata and missing sentinel":                            publicMediaContractReadyAsset,
		"latest matching ready asset per file":                                 publicMediaContractLatestAsset,
		"batch asset lookup filters state and IDs":                             publicMediaContractBatchAssets,
		"generation lookup filters state and IDs":                              publicMediaContractGenerationLookup,
		"file projections omit pending deletion":                               publicMediaContractPendingFiles,
		"derivative projections preserve target kinds":                         publicMediaContractDerivativeTargets,
		"owner and exact content relation":                                     publicMediaContractContentOwnership,
		"track lookup requires exact release":                                  publicMediaContractTrackRelease,
		"content owner witness and locked recheck":                             publicMediaContractOwnerRecheck,
		"content relation projection ignores policy and filters pending files": publicMediaContractRelationProjection,
		"share link witness includes expiry":                                   publicMediaContractShareExpiry,
		"owner share lock blocks a writer":                                     publicMediaContractOwnerLock,
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
func File() FileRecord {
	filename := "source"
	duration := 3
	return FileRecord{MediaFile: public.MediaFile{ID: uuid.NewString(), Extension: "webp", MimeType: "image/webp", FileSize: 1024, FileName: &filename, DurationSeconds: &duration}}
}
func Asset(fileID, kind, status string, at time.Time) AssetRecord {
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	extension, mime := "webp", "image/webp"
	if kind == "spectrogram" {
		extension, mime = "png", "image/png"
	}
	return AssetRecord{ReadyAsset: public.ReadyAsset{AssetID: id, SourceFileID: &fileID, Kind: kind, Extension: extension, MimeType: mime, FileSize: 64, SHA256: digest[:], Disposition: "inline"}, Status: status, CreatedAt: at}
}
func Owner(kind string) public.OwnerWitness {
	status := "POST_STATUS_PUBLISHED"
	if kind == "release" {
		status = "RELEASE_STATUS_PUBLISHED"
	}
	return public.OwnerWitness{ResourceType: kind, ResourceID: uuid.NewString(), DocumentID: uuid.NewString(), Status: status}
}
func FakeHarness(s *Store) Harness {
	return Harness{Harness: mutationtest.FakeHarness[public.Store](s), SeedFile: func(_ *testing.T, row FileRecord) { s.Files[row.ID] = row }, SeedAsset: func(_ *testing.T, row AssetRecord) { s.Assets[row.AssetID] = row }, SeedGeneration: func(_ *testing.T, row GenerationRecord) { s.Generations[row.GenerationID] = row }, SeedDerivative: func(_ *testing.T, row public.Derivative) { s.DerivativeRows = append(s.DerivativeRows, row) }, SeedOwner: func(_ *testing.T, row public.OwnerWitness) { s.OwnerRows[row.ResourceType+":"+row.ResourceID] = row }, SeedRelation: func(_ *testing.T, row Relation) {
		s.Relations = append(s.Relations, row)
		s.BlockOwners[row.BlockID] = []public.OwnerWitness{row.Owner}
	}, SeedTrack: func(_ *testing.T, row Track) { s.Tracks[row.ID] = row }, SeedShareLink: func(_ *testing.T, row public.ShareLink) { s.LinksByID[row.ID] = row }, UpdateOwner: func(_ context.Context, store public.Store, row public.OwnerWitness) error {
		store.(*Store).OwnerRows[row.ResourceType+":"+row.ResourceID] = row
		return nil
	}}
}

func publicMediaContractReadyAsset(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	a := Asset(f.ID, "thumbnail", "ready", time.Now().UTC())
	h.SeedAsset(t, a)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s public.Store) error {
		row, err := s.ReadyAsset(ctx, " "+a.AssetID+" ")
		require.NoError(t, err)
		require.Equal(t, a.ReadyAsset, row)
		_, err = s.ReadyAsset(ctx, uuid.NewString())
		require.ErrorIs(t, err, public.ErrAssetNotFound)
		return nil
	}))
}

func publicMediaContractLatestAsset(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	at := time.Now().UTC()
	older := Asset(f.ID, "thumbnail", "ready", at.Add(-time.Minute))
	a := Asset(f.ID, "thumbnail", "ready", at)
	a.AssetID = "11111111-1111-4111-8111-111111111111"
	b := Asset(f.ID, "thumbnail", "ready", at)
	b.AssetID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	wrongKind := Asset(f.ID, "spectrogram", "ready", at.Add(time.Minute))
	pending := Asset(f.ID, "thumbnail", "allocated", at.Add(2*time.Minute))
	for _, row := range []AssetRecord{older, b, a, wrongKind, pending} {
		h.SeedAsset(t, row)
	}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s public.Store) error {
		rows, err := s.ReadyAssetsForFiles(ctx, []string{"", f.ID, " " + f.ID + " "}, "thumbnail")
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.Equal(t, b.ReadyAsset, rows[f.ID])
		return nil
	}))
}

func publicMediaContractBatchAssets(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	ready := Asset(f.ID, "thumbnail", "ready", time.Now())
	allocated := Asset(f.ID, "spectrogram", "allocated", time.Now())
	h.SeedAsset(t, ready)
	h.SeedAsset(t, allocated)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s public.Store) error {
		rows, err := s.ReadyAssets(ctx, []string{"", ready.AssetID, " " + ready.AssetID + " ", allocated.AssetID, uuid.NewString()})
		require.NoError(t, err)
		require.Equal(t, map[string]public.ReadyAsset{ready.AssetID: ready.ReadyAsset}, rows)
		rows, err = s.ReadyAssets(ctx, nil)
		require.Empty(t, rows)
		return err
	}))
}

func publicMediaContractGenerationLookup(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	ready := GenerationRecord{ReadyGeneration: public.ReadyGeneration{FileID: f.ID, GenerationID: uuid.NewString(), ManifestName: "master.m3u8"}, Status: "ready"}
	retired := ready
	retired.GenerationID = uuid.NewString()
	retired.Status = "retired"
	h.SeedGeneration(t, ready)
	h.SeedGeneration(t, retired)
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s public.Store) error {
		rows, err := s.ReadyGenerations(ctx, []string{"", ready.GenerationID, " " + ready.GenerationID + " ", retired.GenerationID})
		require.NoError(t, err)
		require.Equal(t, map[string]public.ReadyGeneration{ready.GenerationID: ready.ReadyGeneration}, rows)
		return nil
	}))
}

func publicMediaContractPendingFiles(t *testing.T, h Harness) {
	ready, pending, other := File(), File(), File()
	pending.PendingDeletion = true
	for _, f := range []FileRecord{ready, pending, other} {
		h.SeedFile(t, f)
	}
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s public.Store) error {
		rows, err := s.MediaFiles(ctx, []string{ready.ID, ready.ID, pending.ID})
		require.NoError(t, err)
		require.Equal(t, []public.MediaFile{ready.MediaFile}, rows)
		return nil
	}))
}

func publicMediaContractDerivativeTargets(t *testing.T, h Harness) {
	f, other := File(), File()
	h.SeedFile(t, f)
	h.SeedFile(t, other)
	asset := Asset(f.ID, "thumbnail", "ready", time.Now())
	h.SeedAsset(t, asset)
	g := GenerationRecord{ReadyGeneration: public.ReadyGeneration{FileID: f.ID, GenerationID: uuid.NewString(), ManifestName: "master.m3u8"}, Status: "ready"}
	h.SeedGeneration(t, g)
	rows := []public.Derivative{{FileID: f.ID, Type: "FILE_DERIVATIVE_TYPE_THUMBNAIL", AssetID: &asset.AssetID}, {FileID: f.ID, Type: "FILE_DERIVATIVE_TYPE_HLS", MediaGenerationID: &g.GenerationID}}
	for _, row := range rows {
		h.SeedDerivative(t, row)
	}
	h.SeedDerivative(t, public.Derivative{FileID: other.ID, Type: "FILE_DERIVATIVE_TYPE_THUMBNAIL", AssetID: &asset.AssetID})
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s public.Store) error {
		got, err := s.Derivatives(ctx, []string{f.ID})
		require.NoError(t, err)
		require.ElementsMatch(t, rows, got)
		return nil
	}))
}

func publicMediaContractContentOwnership(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	owner := Owner("post")
	h.SeedOwner(t, owner)
	relation := Relation{BlockID: uuid.NewString(), ReferencePath: "file", FileID: f.ID, Kind: "file", Owner: owner, Audience: mediaasset.FileDownloadAudiencePublic}
	h.SeedRelation(t, relation)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s public.Store) error {
		status, found, err := s.LockContentOwner(ctx, public.EntityPost, owner.ResourceID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, owner.Status, status)
		source, status, found, err := s.ResolveContentSource(ctx, public.EntityPost, owner.ResourceID, relation.BlockID, "file", true)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, owner.Status, status)
		require.Equal(t, f.ID, source.FileID)
		require.Equal(t, f.FileSize, source.FileSize)
		require.Equal(t, mediaasset.FileDownloadPolicyContentBlock, source.PolicyKind)
		for _, args := range [][3]string{{uuid.NewString(), relation.BlockID, "file"}, {owner.ResourceID, uuid.NewString(), "file"}, {owner.ResourceID, relation.BlockID, "other"}} {
			_, _, ok, err := s.ResolveContentSource(ctx, public.EntityPost, args[0], args[1], args[2], false)
			require.NoError(t, err)
			require.False(t, ok)
		}
		_, _, err = s.LockContentOwner(ctx, public.EntityUnspecified, owner.ResourceID)
		require.Error(t, err)
		return nil
	}))
}

func publicMediaContractTrackRelease(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	owner := Owner("release")
	h.SeedOwner(t, owner)
	track := Track{ID: uuid.NewString(), ReleaseID: owner.ResourceID, FileID: f.ID, Audience: mediaasset.FileDownloadAudiencePublic}
	h.SeedTrack(t, track)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s public.Store) error {
		source, status, found, err := s.ResolveTrackSource(ctx, owner.ResourceID, track.ID, true)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, owner.Status, status)
		require.Equal(t, f.ID, source.FileID)
		require.Equal(t, track.ID, source.PolicyKey)
		_, _, found, err = s.ResolveTrackSource(ctx, uuid.NewString(), track.ID, false)
		require.NoError(t, err)
		require.False(t, found)
		status, found, err = s.LockReleaseOwner(ctx, owner.ResourceID)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, owner.Status, status)
		return nil
	}))
}

func publicMediaContractOwnerRecheck(t *testing.T, h Harness) {
	f := File()
	h.SeedFile(t, f)
	owner := Owner("post")
	h.SeedOwner(t, owner)
	relation := Relation{BlockID: uuid.NewString(), ReferencePath: "file", FileID: f.ID, Kind: "file", Owner: owner, Audience: mediaasset.FileDownloadAudienceDisabled}
	h.SeedRelation(t, relation)
	selector := mediaasset.ContentBlockDownloadSelector{BlockID: relation.BlockID, ReferencePath: "file"}
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s public.Store) error {
		expected := map[string]public.OwnerWitness{relation.BlockID: owner}
		rows, err := s.ContentOwners(ctx, []mediaasset.ContentBlockDownloadSelector{selector, {BlockID: uuid.NewString()}})
		require.NoError(t, err)
		require.Equal(t, expected, rows)
		rows, err = s.LockContentOwners(ctx, expected)
		require.NoError(t, err)
		require.Equal(t, expected, rows)
		changed := owner
		changed.Status = "POST_STATUS_DRAFT"
		rows, err = s.LockContentOwners(ctx, map[string]public.OwnerWitness{relation.BlockID: changed})
		require.NoError(t, err)
		require.Empty(t, rows)
		return nil
	}))
}

func publicMediaContractRelationProjection(t *testing.T, h Harness) {
	owner := Owner("post")
	h.SeedOwner(t, owner)
	ready, pending := File(), File()
	pending.PendingDeletion = true
	h.SeedFile(t, ready)
	h.SeedFile(t, pending)
	block := uuid.NewString()
	h.SeedRelation(t, Relation{BlockID: block, ReferencePath: "file", FileID: ready.ID, Kind: "file", Owner: owner, Audience: mediaasset.FileDownloadAudienceDisabled})
	h.SeedRelation(t, Relation{BlockID: uuid.NewString(), ReferencePath: "file", FileID: pending.ID, Kind: "file", Owner: owner, Audience: mediaasset.FileDownloadAudiencePublic})
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s public.Store) error {
		rows, err := s.ContentRelations(ctx, []mediaasset.ContentBlockDownloadSelector{{BlockID: block, ReferencePath: "file"}, {BlockID: block, ReferencePath: "other"}}, true)
		require.NoError(t, err)
		require.Equal(t, map[string]string{mediaasset.ContentBlockDownloadPolicyKey(block, "file"): ready.ID}, rows)
		return nil
	}))
}

func publicMediaContractShareExpiry(t *testing.T, h Harness) {
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	owner := Owner("post")
	h.SeedOwner(t, owner)
	link := public.ShareLink{ID: uuid.NewString(), EntityType: "SHARE_LINK_ENTITY_TYPE_POST", EntityID: owner.ResourceID, ExpiresAt: &expires}
	h.SeedShareLink(t, link)
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s public.Store) error {
		row, found, err := s.ShareLink(ctx, link.ID, true)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, link.ID, row.ID)
		require.Equal(t, link.EntityType, row.EntityType)
		require.Equal(t, link.EntityID, row.EntityID)
		require.True(t, expires.Equal(*row.ExpiresAt))
		_, found, err = s.ShareLink(ctx, uuid.NewString(), false)
		require.NoError(t, err)
		require.False(t, found)
		return nil
	}))
}

func publicMediaContractOwnerLock(t *testing.T, h Harness) {
	owner := Owner("post")
	h.SeedOwner(t, owner)
	next := owner
	next.Status = "POST_STATUS_DRAFT"
	require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s public.Store) error {
		_, _, err := s.LockContentOwner(ctx, public.EntityPost, owner.ResourceID)
		return err
	}, func(context.Context, public.Store) error { return nil }, func(ctx context.Context, s public.Store) error { return h.UpdateOwner(ctx, s, next) }))
	require.NoError(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s public.Store) error {
		status, ok, err := s.LockContentOwner(ctx, public.EntityPost, owner.ResourceID)
		require.True(t, ok)
		require.Equal(t, next.Status, status)
		return err
	}))
}
