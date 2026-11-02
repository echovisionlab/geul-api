package public

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/label"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/require"
)

// Every public Label status maps from the stored value with its own name, so
// a new API value fails here until it is mapped.
func TestPublicLabelStatusCoversEveryAPIStatus(t *testing.T) {
	for value, name := range openv1.LabelStatus_name {
		require.Equal(t, openv1.LabelStatus(value), publicLabelStatus(label.Status(name)), name)
	}
	require.Equal(t, openv1.LabelStatus_LABEL_STATUS_UNSPECIFIED, publicLabelStatus("LABEL_STATUS_ARCHIVED"))
}
