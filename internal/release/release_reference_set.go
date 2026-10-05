package release

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func setReleaseReferenceSet[T interface{}](
	ctx context.Context,
	service *ReleaseService,
	releaseID string,
	relation string,
	ids []string,
	observed *managev1.StringIdSnapshot,
	newReference func(releaseID, referenceID string) *T,
) (*connect.Response[managev1.SuccessResponse], error) {
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, releaseID); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, service.spiceDB, releaseID, releaseActionManage); err != nil {
			return err
		}
		if err := requireObservedRelationSnapshot(observed); err != nil {
			return err
		}
		table, column := releaseReferenceAuditTable(relation)
		var existing []string
		if err := tx.Table(table).Where("release_id = ?", releaseID).Pluck(column, &existing).Error; err != nil {
			return err
		}
		removed, added := mergeObservedIDs(existing, observed.Ids, ids)
		if len(removed) == 0 && len(added) == 0 {
			return nil
		}
		if len(removed) > 0 {
			query := "DELETE FROM " + table + " WHERE release_id = ? AND " + column + " IN ?"
			if err := tx.Exec(query, releaseID, removed).Error; err != nil {
				return err
			}
		}
		for _, id := range added {
			if err := tx.Create(newReference(releaseID, id)).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.Release{}).
			Where("id = ?", releaseID).
			Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		return service.appendReleaseMetadataAudit(ctx, tx, releaseID, []string{relation})
	})
	if err != nil {
		return nil, classifyReleaseMutationError(err, releaseID)
	}

	publishContentUpdatedEvent(
		ctx,
		service.asyncPublisher,
		buildReleaseRelationMutationContentUpdatedEvent(
			releaseID,
			[]string{"relations." + relation},
		),
	)
	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}

func requireObservedRelationSnapshot[T any](observed *T) error {
	if observed == nil {
		return errs.InvalidArgument("observed", "is required")
	}
	return nil
}

func mergeObservedIDs(current, observed, desired []string) (removed, added []string) {
	observedSet := make(map[string]struct{}, len(observed))
	desiredSet := make(map[string]struct{}, len(desired))
	currentSet := make(map[string]struct{}, len(current))
	for _, id := range observed {
		observedSet[id] = struct{}{}
	}
	for _, id := range desired {
		desiredSet[id] = struct{}{}
	}
	for _, id := range current {
		currentSet[id] = struct{}{}
		if _, hadObserved := observedSet[id]; hadObserved {
			if _, stillDesired := desiredSet[id]; !stillDesired {
				removed = append(removed, id)
			}
		}
	}
	for _, id := range desired {
		if _, hadObserved := observedSet[id]; hadObserved {
			continue
		}
		if _, exists := currentSet[id]; !exists {
			added = append(added, id)
			currentSet[id] = struct{}{}
		}
	}
	return removed, added
}

func releaseReferenceAuditTable(relation string) (string, string) {
	switch relation {
	case "categories":
		return "release_category", "category_id"
	case "genres":
		return "release_genre", "genre_id"
	case "styles":
		return "release_style", "style_id"
	default:
		panic("unsupported release reference relation")
	}
}
