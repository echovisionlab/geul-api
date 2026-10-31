package artist

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func IsValidUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }

func ensureSlugAvailable(ctx context.Context, db *gorm.DB, value structured.Value, entity, slug, excludeID string) error {
	if slug == "" {
		return nil
	}
	query := db.WithContext(ctx).Model(value).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return errors.Internal(err)
	}
	if count != 0 {
		return errors.SlugAlreadyExists(entity, slug)
	}
	return nil
}

func loadReadyManageOgAssetRefs(ctx context.Context, runtime Runtime, db *gorm.DB, candidates ...*string) (map[string]*commonv1.AssetRef, error) {
	ids := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate != nil {
			id := strings.TrimSpace(*candidate)
			if id != "" {
				if _, ok := seen[id]; !ok {
					seen[id] = struct{}{}
					ids = append(ids, id)
				}
			}
		}
	}
	return runtime.ResolveReadyAssetRefs(ctx, db, ids)
}

func manageOgAssetFromReadyMap(ready map[string]*commonv1.AssetRef, candidates ...*string) *commonv1.AssetRef {
	for _, candidate := range candidates {
		if candidate != nil {
			if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
				return asset
			}
		}
	}
	return nil
}

func readyManageOgAssetRef(ctx context.Context, runtime Runtime, db *gorm.DB, candidates ...*string) (*commonv1.AssetRef, error) {
	ready, err := loadReadyManageOgAssetRefs(ctx, runtime, db, candidates...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidates...), nil
}

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func buildContentUpdatedEvent(entityType managev1.ContentEntityType, entityID string, fields []*managev1.ContentUpdatedField) *managev1.ContentUpdatedEvent {
	if entityID == "" || len(fields) == 0 {
		return nil
	}
	return &managev1.ContentUpdatedEvent{EntityType: entityType, EntityId: entityID, Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, ChangedFields: fields, TimestampMs: time.Now().UnixMilli()}
}

func buildManageStateTransitionContentUpdatedEvent(entityType managev1.ContentEntityType, entityID string, paths []string) *managev1.ContentUpdatedEvent {
	fields := make([]*managev1.ContentUpdatedField, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		fields = append(fields, &managev1.ContentUpdatedField{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE})
	}
	return buildContentUpdatedEvent(entityType, entityID, fields)
}

func buildManageMediaMutationContentUpdatedEvent(entityType managev1.ContentEntityType, entityID, path string) *managev1.ContentUpdatedEvent {
	return buildContentUpdatedEvent(entityType, entityID, []*managev1.ContentUpdatedField{{Path: path, Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA}})
}

// publishContentUpdatedEvent schedules content.updated in the caller's
// mutation transaction so the signal commits or rolls back with the data.
func publishContentUpdatedEvent(
	ctx context.Context,
	tx *gorm.DB,
	publisher AsyncPublisher,
	event *managev1.ContentUpdatedEvent,
) error {
	return contentsignal.PublishInTransaction(ctx, tx, publisher, event)
}

func LabelSourceTitleSQL(alias string) string {
	return sourceTitleSQL(alias, "label", "label_translation", "lt")
}
func ReleaseSourceTitleSQL(alias string) string {
	return sourceTitleSQL(alias, "release", "release_translation", "rt")
}
func WorkSourceTitleSQL(alias string) string {
	return sourceTitleSQL(alias, "work", "work_translation", "wt")
}
func sourceTitleSQL(alias, entity, table, translationAlias string) string {
	if strings.TrimSpace(alias) == "" {
		alias = entity
	}
	return "COALESCE((SELECT " + translationAlias + ".title FROM " + table + " AS " + translationAlias + " JOIN " + entity + " AS source ON source.id = " + translationAlias + ".entity_id AND source.source_locale = " + translationAlias + ".locale WHERE " + translationAlias + ".entity_id = " + alias + ".id LIMIT 1), '')"
}
