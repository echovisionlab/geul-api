package mediaasset_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLifecycleRequiresUnits(t *testing.T) {
	require.Panics(t, func() { mediaasset.NewLifecycle(nil, "") })
}
func TestAllocationValidatesBeforeStorage(t *testing.T) {
	for name, change := range map[string]func(*mediaasset.Allocation){
		"unknown kind":            func(v *mediaasset.Allocation) { v.Kind = "unknown" },
		"missing extension":       func(v *mediaasset.Allocation) { v.Extension = "" },
		"mismatched extension":    func(v *mediaasset.Allocation) { v.Extension = "png" },
		"unknown MIME":            func(v *mediaasset.Allocation) { v.MimeType = "application/x-unknown" },
		"invalid source":          func(v *mediaasset.Allocation) { v.SourceFileID = new("invalid") },
		"attachment disposition":  func(v *mediaasset.Allocation) { v.Disposition = mediaasset.DispositionAttachment },
		"unspecified disposition": func(v *mediaasset.Allocation) { v.Disposition = mediaasset.DispositionUnspecified },
		"download filename":       func(v *mediaasset.Allocation) { v.DownloadFilename = new("image.webp") },
	} {
		t.Run(name, func(t *testing.T) {
			service, store, _ := lifecycleFixture(t)
			storageFailure := errors.New("storage must not be called")
			store.Fail = storageFailure
			input := allocation()
			change(&input)
			_, _, err := service.AllocatePublicAsset(t.Context(), input)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
			require.NotErrorIs(t, err, storageFailure)
		})
	}
	for _, tc := range []struct {
		kind, extension, mime string
		valid                 bool
	}{{"image", "webp", "image/webp", true}, {"image", "pdf", "application/pdf", false}, {"mesh", "glb", "model/gltf-binary", true}, {"mesh", "webp", "image/webp", false}, {"waveform", "json", "application/json", true}, {"waveform", "png", "image/png", false}, {"spectrogram", "png", "image/png", true}, {"thumbnail", "png", "image/png", false}, {"avatar", "png", "image/png", false}} {
		t.Run(tc.kind+tc.extension, func(t *testing.T) {
			input := allocation()
			input.Kind = tc.kind
			input.Extension = tc.extension
			input.MimeType = tc.mime
			err := mediaasset.ValidateAllocation(input)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
func TestCompletionValidatesBeforeStorage(t *testing.T) {
	service, _, _ := lifecycleFixture(t)
	for _, input := range []*mediaasset.WrittenAsset{nil, {AssetID: "invalid"}, {AssetID: uuid.NewString(), FileSize: 0, SHA256: make([]byte, 32)}, {AssetID: uuid.NewString(), FileSize: 1, SHA256: []byte{1}}} {
		_, err := service.CompletePublicAsset(t.Context(), input)
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	}
	for _, input := range []*mediaasset.WrittenGeneration{nil, {GenerationID: "invalid"}, {GenerationID: uuid.NewString(), ManifestSHA256: []byte{1}, ObjectCount: 1, TotalSize: 1}} {
		_, err := service.CompleteMediaGeneration(t.Context(), input)
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	}
}
func TestMissingOrUnreadyAssetsDoNotEmitReferences(t *testing.T) {
	service, _, _ := lifecycleFixture(t)
	_, err := service.ReadyAssetRef(t.Context(), uuid.NewString())
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
	asset, _, err := service.AllocatePublicAsset(t.Context(), allocation())
	require.NoError(t, err)
	_, err = service.ReadyAssetRef(t.Context(), asset.ID)
	require.Equal(t, connect.CodeFailedPrecondition, errs.Wrap(err).Code())
	_, err = service.ReadyAssetRefForSourceFile(t.Context(), uuid.NewString(), "unknown")
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Error(t, service.BindPublicAsset(t.Context(), mediaasset.Binding{}))
	require.Error(t, service.ReleasePublicAssetBindings(t.Context(), "", "owner", "image"))
	require.Error(t, service.ReleaseExactPublicAssetBindings(t.Context(), "post", "", nil))
}
func TestValueNormalizationPreservesEmptyOptionals(t *testing.T) {
	require.Nil(t, mediaasset.NormalizeOptionalString(nil))
	require.Nil(t, mediaasset.NormalizeOptionalString(new(" ")))
	require.Equal(t, "value", *mediaasset.NormalizeOptionalString(new(" value ")))
	require.Equal(t, "image/webp", mediaasset.CanonicalMimeType(" image/webp; charset=binary "))
}
