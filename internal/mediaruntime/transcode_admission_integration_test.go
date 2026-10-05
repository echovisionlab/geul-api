//go:build integration

package mediaruntime

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcoding/handler"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTranscodeAdmissionReadsDurableCancellationIntegration(t *testing.T) {
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true})
	identity := handler.JobIdentity{
		EventID: uuid.NewString(), EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST,
		EntityID: uuid.NewString(), FileID: uuid.NewString(),
	}
	_, err := pg.SQLDB.ExecContext(t.Context(), `INSERT INTO transcode_job (event_id, queue_name, entity_type, entity_id, file_id, payload) VALUES ($1, 'transcoder.audio', $2, $3, $4, ''::bytea)`, identity.EventID, identity.EntityType.String(), identity.EntityID, identity.FileID)
	require.NoError(t, err)
	decision, err := newTranscodeJobAdmission(pg.SQLDB).Admit(t.Context(), identity)
	require.NoError(t, err)
	require.Equal(t, handler.JobAdmissionProceed, decision)

	_, err = pg.SQLDB.ExecContext(t.Context(), `UPDATE transcode_job SET status = $1 WHERE event_id = $2`, transcode.StatusCancelled, identity.EventID)
	require.NoError(t, err)
	// Construct a fresh reader after the cancellation commit, as on restart.
	decision, err = newTranscodeJobAdmission(pg.SQLDB).Admit(t.Context(), identity)
	require.NoError(t, err)
	require.Equal(t, handler.JobAdmissionSettled, decision)

	wrong := identity
	wrong.FileID = uuid.NewString()
	decision, err = newTranscodeJobAdmission(pg.SQLDB).Admit(t.Context(), wrong)
	require.NoError(t, err)
	require.Equal(t, handler.JobAdmissionIdentityMismatch, decision)

	_, err = pg.SQLDB.ExecContext(t.Context(), `DELETE FROM transcode_job WHERE event_id = $1`, identity.EventID)
	require.NoError(t, err)
	decision, err = newTranscodeJobAdmission(pg.SQLDB).Admit(t.Context(), identity)
	require.NoError(t, err)
	require.Equal(t, handler.JobAdmissionSettled, decision)
}
