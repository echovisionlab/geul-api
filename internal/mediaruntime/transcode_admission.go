package mediaruntime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcoding/handler"
)

type transcodeJobAdmission struct {
	db *sql.DB
}

func newTranscodeJobAdmission(db *sql.DB) handler.JobAdmission {
	return transcodeJobAdmission{db: db}
}

func (a transcodeJobAdmission) Admit(ctx context.Context, identity handler.JobIdentity) (handler.JobAdmissionDecision, error) {
	var stored handler.JobIdentity
	var entityType string
	var status string
	err := a.db.QueryRowContext(ctx, `SELECT entity_type, entity_id, file_id, status FROM transcode_job WHERE event_id = $1`, identity.EventID).
		Scan(&entityType, &stored.EntityID, &stored.FileID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return handler.JobAdmissionSettled, nil
	}
	if err != nil {
		return 0, err
	}
	if entityType != identity.EntityType.String() || stored.EntityID != identity.EntityID || stored.FileID != identity.FileID {
		return handler.JobAdmissionIdentityMismatch, nil
	}
	if status == transcode.StatusQueued || status == transcode.StatusProcessing {
		return handler.JobAdmissionProceed, nil
	}
	switch status {
	case transcode.StatusCancelled, transcode.StatusCompleted, transcode.StatusFailedTerminal:
		return handler.JobAdmissionSettled, nil
	default:
		return 0, fmt.Errorf("unrecognized transcode job status %q", status)
	}
}
