package filemedia

import (
	"context"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type FileDeletePublisher interface {
	PublishFileDelete(context.Context, *managev1.FileDeleteEvent) error
}
