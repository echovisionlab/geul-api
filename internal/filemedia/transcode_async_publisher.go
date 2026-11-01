package filemedia

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *Service) enqueueStableFileIngestTranscode(ctx context.Context, file File, entityType managev1.TranscodeEntityType, entityID string, kind domain.IngestTranscodeKind) error {
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		var job *domain.IngestTranscodeCommand
		var publish bool
		var err error
		if kind == domain.IngestTranscodeAudio {
			job, publish, err = newStableFileIngestAudioTranscodeJob(ctx, store, file, entityType, entityID)
		} else {
			job, publish, err = newStableFileIngestVideoTranscodeJob(ctx, store, file, entityType, entityID)
		}
		if err != nil || !publish {
			return err
		}
		if err := store.EnqueueIngestTranscode(ctx, *job); err != nil {
			return err
		}
		return nil
	})
}
