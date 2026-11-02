package transcodepostgres

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestTranscodeJobModelOmitsBrokerTransportState(t *testing.T) {
	t.Parallel()

	modelType := reflect.TypeFor[model.TranscodeJob]()
	for _, fieldName := range []string{"Attempts", "LastHeartbeat"} {
		_, found := modelType.FieldByName(fieldName)
		require.False(t, found, "transcode_job must not mirror queue transport field %s", fieldName)
	}
}
