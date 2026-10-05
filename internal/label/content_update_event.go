package label

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func publishLabelContentUpdated(ctx context.Context, publisher AsyncPublisher, event *managev1.ContentUpdatedEvent) {
	if publisher == nil || event == nil {
		return
	}
	if err := publisher.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, event); err != nil {
		slog.Warn("publish Label content update signal", "error", err, "label_id", event.EntityId)
	}
}

func buildLabelMediaMutationContentUpdatedEvent(labelID string) *managev1.ContentUpdatedEvent {
	if labelID == "" {
		return nil
	}
	return &managev1.ContentUpdatedEvent{
		EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_LABEL,
		EntityId:   labelID,
		Source:     managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "media.image", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_MEDIA,
		}},
		TimestampMs: time.Now().UnixMilli(),
	}
}

func buildLabelStateTransitionContentUpdatedEvent(labelID string) *managev1.ContentUpdatedEvent {
	if labelID == "" {
		return nil
	}
	return &managev1.ContentUpdatedEvent{EntityType: managev1.ContentEntityType_CONTENT_ENTITY_TYPE_LABEL, EntityId: labelID, Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE, ChangedFields: []*managev1.ContentUpdatedField{{Path: "state.status", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE}, {Path: "state.published_at", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_STATE}}, TimestampMs: time.Now().UnixMilli()}
}

// buildLabelContentUpdatedEvent emits the exact locale fence consumed by the
// collaboration runtime. Source writes advance the shared document revision;
// target writes carry only their opaque target revision and a target deletion
// carries locale_exists=false.
func buildLabelContentUpdatedEvent(
	labelID string,
	fields []string,
	revision string,
	contributors []string,
	source managev1.ContentUpdateSource,
	locale string,
	localeExists bool,
	targetRevision *string,
	documentStateChanged bool,
) *managev1.ContentUpdatedEvent {
	labelID = strings.TrimSpace(labelID)
	revision = strings.TrimSpace(revision)
	locale = strings.TrimSpace(locale)
	if labelID == "" || revision == "" || locale == "" || len(fields) == 0 {
		return nil
	}
	switch {
	case !localeExists && (targetRevision != nil || documentStateChanged):
		return nil
	case localeExists && targetRevision != nil && (strings.TrimSpace(*targetRevision) == "" || documentStateChanged):
		return nil
	case localeExists && targetRevision == nil && !documentStateChanged:
		return nil
	}
	return &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_LABEL,
		EntityId:             labelID,
		Source:               source,
		ChangedFields:        labelContentUpdatedFields(fields),
		ContributorMemberIds: normalizeLabelEventContributors(contributors),
		DocumentStateChanged: documentStateChanged,
		DocumentRevision:     &revision,
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TargetRevision:       cloneLabelEventTargetRevision(targetRevision),
		TimestampMs:          time.Now().UnixMilli(),
	}
}

func labelContentUpdatedFields(fields []string) []*managev1.ContentUpdatedField {
	result := make([]*managev1.ContentUpdatedField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		path := strings.TrimSpace(field)
		if path == "" {
			continue
		}
		kind := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT
		switch path {
		case "content":
			path = "document.content"
		case "title":
		default:
			kind = managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, &managev1.ContentUpdatedField{Path: path, Kind: kind})
	}
	return result
}

func normalizeLabelEventContributors(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func cloneLabelEventTargetRevision(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
