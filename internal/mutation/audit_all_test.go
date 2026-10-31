package mutation

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAllAppendsTheRecordsOfOneChangeInOrder(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, _ := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "several")
	record := func(name string) Audit {
		return RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, uuid.NewString(), name)
		})
	}
	spec.Audit = func(note) Audit { return All(record("first"), Unchanged(), record("second")) }

	_, err := Run(memberRequestContext(t), runner, spec)
	require.NoError(t, err)
	require.Len(t, recorder.Records(), 2)

	spec.Audit = func(note) Audit { return All() }
	_, err = Run(memberRequestContext(t), runner, spec)
	require.ErrorContains(t, err, "return Unchanged")
}
