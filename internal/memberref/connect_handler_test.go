package memberref

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestReferenceMessageKeepsAssetWireValuesAndPresence(t *testing.T) {
	filename := ""
	summary := &Summary{
		ID: "member", Nickname: "멤버",
		Avatar: &Asset{ID: "asset", URL: "https://cdn.test/a", Extension: "png", MIMEType: "image/png", FileSize: 9007199254740993, SHA256: []byte{1, 2}, Disposition: AssetDisposition(-7), DownloadFilename: &filename},
	}
	encoded, err := (protojson.MarshalOptions{EmitUnpopulated: true}).Marshal(ReferenceMessage(summary))
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"member","nickname":"멤버","avatarAsset":{"assetId":"asset","url":"https://cdn.test/a","extension":"png","mimeType":"image/png","fileSize":"9007199254740993","sha256":"AQI=","disposition":-7,"downloadFilename":""},"deleted":false}`, string(encoded))
	require.Nil(t, ReferenceMessage(nil))
}
