package worker

import (
	"context"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) handleSendBulkEmailBatch(
	ctx context.Context,
	job *managev1.SendBulkEmailBatchEvent,
) error {
	return h.bulkEmail.Handle(ctx, job)
}
