package mq

import (
	"context"
	"fmt"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"gorm.io/gorm"
)

// EnqueueTranslationGenerateWithDB keeps the command invisible to workers until
// the owning translation job transaction commits, and removes both on rollback.
func (p *Publisher) EnqueueTranslationGenerateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	job *managev1.TranslationGenerateEvent,
) error {
	if job == nil {
		return fmt.Errorf("translation generate job is required")
	}
	executor, err := GormTransactionExecutor(tx)
	if err != nil {
		return err
	}
	if _, ok := executor.(gorm.TxCommitter); !ok {
		return fmt.Errorf("translation command requires the owning transaction")
	}
	return EnqueueProtobuf(ctx, executor, eventpkg.QueueTranslationGenerate, job.GetJobId(), job)
}
