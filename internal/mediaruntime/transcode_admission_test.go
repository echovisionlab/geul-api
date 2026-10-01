package mediaruntime

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcoding/handler"
	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

const transcodeAdmissionSelect = `SELECT entity_type, entity_id, file_id, status FROM transcode_job WHERE event_id = $1`

func TestTranscodeJobAdmissionAllowsOnlyMatchingActiveJobs(t *testing.T) {
	for _, status := range []string{transcode.StatusQueued, transcode.StatusProcessing} {
		t.Run(status, func(t *testing.T) {
			admission, mock := newTranscodeAdmissionTest(t)
			mock.ExpectQuery(regexp.QuoteMeta(transcodeAdmissionSelect)).
				WithArgs("event-1").
				WillReturnRows(transcodeAdmissionRow(status))

			decision, err := admission.Admit(context.Background(), transcodeAdmissionIdentity())

			require.NoError(t, err)
			require.Equal(t, handler.JobAdmissionProceed, decision)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestTranscodeJobAdmissionSettlesTerminalAndMissingJobs(t *testing.T) {
	for _, status := range []string{
		transcode.StatusCancelled,
		transcode.StatusCompleted,
		transcode.StatusFailedTerminal,
	} {
		t.Run(status, func(t *testing.T) {
			admission, mock := newTranscodeAdmissionTest(t)
			mock.ExpectQuery(regexp.QuoteMeta(transcodeAdmissionSelect)).
				WithArgs("event-1").
				WillReturnRows(transcodeAdmissionRow(status))

			decision, err := admission.Admit(context.Background(), transcodeAdmissionIdentity())

			require.NoError(t, err)
			require.Equal(t, handler.JobAdmissionSettled, decision)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}

	t.Run("missing", func(t *testing.T) {
		admission, mock := newTranscodeAdmissionTest(t)
		mock.ExpectQuery(regexp.QuoteMeta(transcodeAdmissionSelect)).
			WithArgs("event-1").
			WillReturnError(sql.ErrNoRows)

		decision, err := admission.Admit(context.Background(), transcodeAdmissionIdentity())

		require.NoError(t, err)
		require.Equal(t, handler.JobAdmissionSettled, decision)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestTranscodeJobAdmissionRetriesUnknownStatus(t *testing.T) {
	admission, mock := newTranscodeAdmissionTest(t)
	mock.ExpectQuery(regexp.QuoteMeta(transcodeAdmissionSelect)).
		WithArgs("event-1").
		WillReturnRows(transcodeAdmissionRow("unknown-status"))

	decision, err := admission.Admit(context.Background(), transcodeAdmissionIdentity())

	require.ErrorContains(t, err, "unrecognized transcode job status")
	require.Zero(t, decision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTranscodeJobAdmissionRejectsIdentityMismatch(t *testing.T) {
	admission, mock := newTranscodeAdmissionTest(t)
	row := sqlmock.NewRows([]string{"entity_type", "entity_id", "file_id", "status"}).
		AddRow(apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(), "other-entity", "file-1", transcode.StatusQueued)
	mock.ExpectQuery(regexp.QuoteMeta(transcodeAdmissionSelect)).
		WithArgs("event-1").
		WillReturnRows(row)

	decision, err := admission.Admit(context.Background(), transcodeAdmissionIdentity())

	require.NoError(t, err)
	require.Equal(t, handler.JobAdmissionIdentityMismatch, decision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTranscodeJobAdmissionKeepsStorageReadFailuresRetryable(t *testing.T) {
	admission, mock := newTranscodeAdmissionTest(t)
	cause := errors.New("postgres unavailable")
	mock.ExpectQuery(regexp.QuoteMeta(transcodeAdmissionSelect)).
		WithArgs("event-1").
		WillReturnError(cause)

	decision, err := admission.Admit(context.Background(), transcodeAdmissionIdentity())

	require.ErrorIs(t, err, cause)
	require.Zero(t, decision)
	require.NoError(t, mock.ExpectationsWereMet())
}

func newTranscodeAdmissionTest(t *testing.T) (handler.JobAdmission, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return newTranscodeJobAdmission(db), mock
}

func transcodeAdmissionIdentity() handler.JobIdentity {
	return handler.JobIdentity{
		EventID:    "event-1",
		EntityType: apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK,
		EntityID:   "entity-1",
		FileID:     "file-1",
	}
}

func transcodeAdmissionRow(status string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"entity_type", "entity_id", "file_id", "status"}).
		AddRow(apiv1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(), "entity-1", "file-1", status)
}
