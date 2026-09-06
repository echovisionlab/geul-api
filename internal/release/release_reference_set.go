package release

import (
	"context"
	"slices"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func setReleaseReferenceSet[T interface{}](
	ctx context.Context,
	service *ReleaseService,
	releaseID string,
	relation string,
	ids []string,
	newReference func(releaseID, referenceID string) *T,
) (*connect.Response[managev1.SuccessResponse], error) {
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, releaseID); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, service.spiceDB, releaseID, releaseActionManage); err != nil {
			return err
		}
		table, column := releaseReferenceAuditTable(relation)
		var existing []string
		if err := tx.Table(table).Where("release_id = ?", releaseID).Pluck(column, &existing).Error; err != nil {
			return err
		}
		if sameReleaseReferenceSet(existing, ids) {
			return nil
		}
		if err := tx.Where("release_id = ?", releaseID).Delete(new(T)).Error; err != nil {
			return err
		}
		for _, id := range ids {
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

func sameReleaseReferenceSet(current, next []string) bool {
	if len(current) != len(next) {
		return false
	}
	current = append([]string(nil), current...)
	next = append([]string(nil), next...)
	slices.Sort(current)
	slices.Sort(next)
	return slices.Equal(current, next)
}
