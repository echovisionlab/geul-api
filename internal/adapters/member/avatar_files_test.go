package memberadapter

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/stretchr/testify/require"
)

type avatarPromotion func(context.Context, string) (*mediaasset.Reference, error)

func (f avatarPromotion) PromoteUserAvatarAsset(ctx context.Context, id string) (*mediaasset.Reference, error) {
	return f(ctx, id)
}

func TestAvatarFilesPreservesThePromotedAssetAndOwnsItsChecksum(t *testing.T) {
	t.Parallel()
	checksum := bytes.Repeat([]byte{0x19}, 32)
	source := &mediaasset.Reference{AssetID: "asset-id", URL: "https://cdn.example.com/asset/avatar.webp", Extension: "webp", MimeType: "image/webp", FileSize: 64, SHA256: checksum, Disposition: mediaasset.DispositionInline}
	adapter := AvatarFiles{Files: avatarPromotion(func(_ context.Context, id string) (*mediaasset.Reference, error) {
		require.Equal(t, "source-id", id)
		return source, nil
	})}
	result, err := adapter.PromoteUserAvatarAsset(t.Context(), "source-id")
	require.NoError(t, err)
	require.Equal(t, &member.Asset{ID: "asset-id", URL: "https://cdn.example.com/asset/avatar.webp", Extension: "webp", MIMEType: "image/webp", FileSize: 64, SHA256: bytes.Repeat([]byte{0x19}, 32), Disposition: member.AssetDisposition(1)}, result)
	result.SHA256[0] = 0xff
	require.Equal(t, byte(0x19), source.SHA256[0])
}

func TestAvatarFilesPreservesMissingResultsAndPromotionErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("promotion unavailable")
	for _, err := range []error{nil, failure} {
		adapter := AvatarFiles{Files: avatarPromotion(func(context.Context, string) (*mediaasset.Reference, error) { return nil, err })}
		result, actual := adapter.PromoteUserAvatarAsset(t.Context(), "source-id")
		require.Nil(t, result)
		if err == nil {
			require.NoError(t, actual)
		} else {
			require.ErrorIs(t, actual, err)
		}
	}
}
