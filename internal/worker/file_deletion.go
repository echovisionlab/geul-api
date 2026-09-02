package worker

import (
	"context"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) HandleFileDelete(ctx context.Context, event *managev1.FileDeleteEvent) error {
	return h.fileMediaRuntime.HandleFileDelete(ctx, event)
}
