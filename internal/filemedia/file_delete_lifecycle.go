package filemedia

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

var ErrFileStillReferenced = domain.ErrFileStillReferenced

type FileDeletionMutation = domain.FileDeletionMutation
type DeleteFileCommand struct{ FileID string }

func (s *Service) DeleteFile(ctx context.Context, command DeleteFileCommand) error {
	id := strings.TrimSpace(command.FileID)
	can, err := policyv1.File.Delete(id)
	if err != nil {
		return domainerr.InvalidField("file_id", "must be a canonical resource UUID")
	}
	if err := s.units.Read(ctx, func(store Store) error {
		n, err := store.FileCount(ctx, []string{id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domainerr.ResourceNotFound("file", id)
		}
		return nil
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[*FileDeletionMutation, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (*FileDeletionMutation, error) {
			return domain.MarkFileDeletionRequested(ctx, store, id, now)
		},
		Audit: func(d *FileDeletionMutation) mutation.Audit {
			if d == nil || d.AlreadyPending {
				return mutation.Unchanged()
			}
			return fileDeletedAudit(id)
		},
		Outbox: func(ctx context.Context, store Store, d *FileDeletionMutation) error {
			if d == nil {
				return nil
			}
			return domain.PublishFileDeletionMutation(ctx, store, d, now)
		},
	})
	return err
}
func (s *Service) DeleteFileByID(ctx context.Context, id string) error {
	return s.deleteFileRecordByID(ctx, id)
}
func (s *Service) deleteFileRecordByID(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	now := time.Now().UTC()
	return s.units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		d, err := domain.MarkFileDeletionRequested(ctx, store, id, now)
		if err != nil || d == nil {
			return err
		}
		if !d.AlreadyPending {
			if err := domain.AppendDeletedAudit(ctx, store.DeletionAudit(s.auditWriter), id); err != nil {
				return err
			}
		}
		return domain.PublishFileDeletionMutation(ctx, store, d, now)
	})
}
func RequestFileDeletion(ctx context.Context, units mutation.UnitOfWork[Store], id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if units == nil {
		return fmt.Errorf("file deletion units are required")
	}
	now := time.Now().UTC()
	return units.Transaction(ctx, false, func(unit mutation.Tx[Store]) error {
		store := unit.Store()
		d, err := domain.MarkFileDeletionRequested(ctx, store, id, now)
		if err != nil || d == nil {
			return err
		}
		return domain.PublishFileDeletionMutation(ctx, store, d, now)
	})
}
