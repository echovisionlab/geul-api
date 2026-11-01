package form

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSubmissionResponsePreservesCountryAndOptionalMetadataOnWire(t *testing.T) {
	for _, country := range []*string{nil, new("KR")} {
		response := submissionMessage(Submission{
			ID: "submission-1", FormID: "form-1", CountryCode: country,
			Data: []byte(`{"answer":"value"}`),
		})
		wire, err := protojson.Marshal(response)
		require.NoError(t, err)
		decoded := new(managev1.FormSubmission)
		require.NoError(t, protojson.Unmarshal(wire, decoded))
		require.Equal(t, country, decoded.CountryCode)
		require.Equal(t, "submission-1", decoded.Id)
		require.Equal(t, "form-1", decoded.FormId)
		require.JSONEq(t, `{"answer":"value"}`, string(decoded.Data))
		require.Nil(t, decoded.IpAddress)
		require.Nil(t, decoded.UserAgent)
	}
}
