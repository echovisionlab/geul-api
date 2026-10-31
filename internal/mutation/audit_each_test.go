package mutation

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEachAppendsEveryRecordInOrder(t *testing.T) {
	recorder := &audittest.Recorder{}
	runner, db := newTestRunner(t, recorder)
	var steps []string
	spec := noteSpec(&steps, "twice audited")
	first, second := uuid.NewString(), uuid.NewString()
	tagAudit := func(id string) Audit {
		return RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, id, "tag")
		})
	}
	spec.Audit = func(note) Audit { return Each(tagAudit(first), Unchanged(), tagAudit(second)) }

	_, err := Run(memberRequestContext(t), runner, spec)

	require.NoError(t, err)
	records := recorder.Records()
	require.Len(t, records, 2)
	require.Equal(t, []string{first, second}, []string{records[0].TargetID, records[1].TargetID})
	require.EqualValues(t, 1, noteCount(t, db))
}

func TestEachRequiresARecord(t *testing.T) {
	runner, db := newTestRunner(t, &audittest.Recorder{})
	var steps []string
	spec := noteSpec(&steps, "unaudited")
	spec.Audit = func(note) Audit { return Each() }

	_, err := Run(memberRequestContext(t), runner, spec)

	require.ErrorContains(t, err, "return Unchanged")
	require.Zero(t, noteCount(t, db))
}
