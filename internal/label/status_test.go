package label

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

// Every API Label status maps from the stored value with its own name, so a
// new API value fails here until it is mapped.
func TestLabelStatusToProtoCoversEveryAPIStatus(t *testing.T) {
	for value, name := range managev1.LabelStatus_name {
		require.Equal(t, managev1.LabelStatus(value), labelStatusToProto(model.LabelStatus(name)), name)
	}
	require.Equal(t, managev1.LabelStatus_LABEL_STATUS_UNSPECIFIED, labelStatusToProto("LABEL_STATUS_ARCHIVED"))
}
