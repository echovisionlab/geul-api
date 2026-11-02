package labelpostgres

import (
	"context"
	"strings"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

func (s *store) PublishContentUpdate(ctx context.Context, update label.ContentUpdate) error {
	return contentsignal.PublishInTransaction(ctx, s.tx, s.ports.Publisher, contentUpdatedEvent(update))
}

// contentUpdatedEvent renders a Label's content.updated signal; nil when the
// update describes no observable change.
func contentUpdatedEvent(update label.ContentUpdate) *managev1.ContentUpdatedEvent {
	switch update.Kind {
	case label.MediaUpdate:
		return buildLabelMediaMutationContentUpdatedEvent(update.LabelID)
	case label.StateUpdate:
		return buildLabelStateTransitionContentUpdatedEvent(update.LabelID)
	case label.LocaleUpdate:
		change := update.Locale
		return buildLabelLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID: update.LabelID, Fields: change.Fields, DocumentRevision: change.DocumentRevision,
			Contributors: change.Contributors, Source: updateSource(change.Source), Locale: change.Locale,
			LocaleDeleted: change.LocaleDeleted, TargetRevision: change.TargetRevision,
			DocumentStateChanged: change.DocumentStateChanged,
		})
	default:
		return nil
	}
}

func updateSource(source label.UpdateSource) managev1.ContentUpdateSource {
	switch source {
	case label.SourceCollaboration:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB
	case label.SourceAI:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI
	default:
		return managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_UNSPECIFIED
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

// buildLabelLocaleContentUpdatedEvent emits the exact locale fence consumed by the
// collaboration runtime. Source writes advance the shared document revision;
// target writes carry only their opaque target revision and a target deletion
// carries locale_exists=false.
func buildLabelLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	fields, targetRevision, documentStateChanged := change.Fields, change.TargetRevision, change.DocumentStateChanged
	localeExists := !change.LocaleDeleted
	labelID := strings.TrimSpace(change.EntityID)
	revision := strings.TrimSpace(change.DocumentRevision)
	locale := strings.TrimSpace(change.Locale)
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
		Source:               change.Source,
		ChangedFields:        labelContentUpdatedFields(fields),
		ContributorMemberIds: sliceutil.SortedUniqueTrimmed(change.Contributors),
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

func cloneLabelEventTargetRevision(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
