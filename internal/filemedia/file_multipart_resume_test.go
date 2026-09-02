package filemedia

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestNextMultipartListPartsMarkerRejectsMissingOrRepeatedTruncatedToken(t *testing.T) {
	t.Parallel()

	_, err := nextMultipartListPartsMarker(&s3.ListPartsOutput{
		IsTruncated: aws.Bool(true),
	}, "")
	require.Error(t, err)

	_, err = nextMultipartListPartsMarker(&s3.ListPartsOutput{
		IsTruncated:          aws.Bool(true),
		NextPartNumberMarker: aws.String("1000"),
	}, "1000")
	require.Error(t, err)

	next, err := nextMultipartListPartsMarker(&s3.ListPartsOutput{
		IsTruncated:          aws.Bool(true),
		NextPartNumberMarker: aws.String("2000"),
	}, "1000")
	require.NoError(t, err)
	require.Equal(t, "2000", next)

	next, err = nextMultipartListPartsMarker(&s3.ListPartsOutput{
		IsTruncated: aws.Bool(false),
	}, "2000")
	require.NoError(t, err)
	require.Equal(t, "2000", next)
}
