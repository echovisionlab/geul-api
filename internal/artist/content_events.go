package artist

import (
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/optional"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// buildArtistLocaleContentUpdatedEvent emits the exact locale fence consumed by the
// collaboration runtime. The shared Content Document revision advances only
// for source-locale mutations; target-only changes carry their opaque target
// revision and leave DocumentStateChanged false.
func buildArtistLocaleContentUpdatedEvent(change contentsignal.LocaleChange) *managev1.ContentUpdatedEvent {
	artistID, fields, revision, locale := change.EntityID, change.Fields, change.DocumentRevision, change.Locale
	localeExists, targetRevision, documentStateChanged := !change.LocaleDeleted, change.TargetRevision, change.DocumentStateChanged
	if strings.TrimSpace(artistID) == "" || strings.TrimSpace(revision) == "" || strings.TrimSpace(locale) == "" || len(fields) == 0 {
		return nil
	}
	if !localeExists {
		if targetRevision != nil || documentStateChanged {
			return nil
		}
	} else if targetRevision != nil {
		if strings.TrimSpace(*targetRevision) == "" || documentStateChanged {
			return nil
		}
	} else if !documentStateChanged {
		return nil
	}
	locale = strings.TrimSpace(locale)
	revision = strings.TrimSpace(revision)
	changedFields := artistContentUpdatedFields(fields)
	if len(changedFields) == 0 {
		return nil
	}
	return &managev1.ContentUpdatedEvent{
		EntityType:           managev1.ContentEntityType_CONTENT_ENTITY_TYPE_ARTIST,
		EntityId:             artistID,
		Source:               change.Source,
		ChangedFields:        changedFields,
		ContributorMemberIds: append([]string(nil), change.Contributors...),
		DocumentStateChanged: documentStateChanged,
		DocumentRevision:     &revision,
		Locale:               &locale,
		LocaleExists:         &localeExists,
		TargetRevision:       optional.Clone(targetRevision),
		TimestampMs:          time.Now().UnixMilli(),
	}
}

func artistContentUpdatedFields(fields []string) []*managev1.ContentUpdatedField {
	result := make([]*managev1.ContentUpdatedField, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		path := strings.TrimSpace(field)
		if path == "" {
			continue
		}
		if path == "content" {
			path = "document.content"
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		kind := managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT
		if path != "document.content" && path != "title" {
			kind = managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION
		}
		result = append(result, &managev1.ContentUpdatedField{Path: path, Kind: kind})
	}
	return result
}
