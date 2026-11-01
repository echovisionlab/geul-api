package filemediaqueue

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type PolicyPublisher struct{ publisher any }

func Policy(publisher any) PolicyPublisher { return PolicyPublisher{publisher} }
func (p PolicyPublisher) PublishPolicyChange(ctx context.Context, tx *gorm.DB, signal domain.PolicyChangeSignal) error {
	event, err := PolicyChangedEvent(signal)
	if err != nil {
		return err
	}
	publisher, ok := p.publisher.(contentsignal.Publisher)
	if !ok {
		return contentsignal.ErrPublisherRequired
	}
	return contentsignal.PublishInTransaction(ctx, tx, publisher, event)
}
func PolicyChangedEvent(relation domain.PolicyChangeSignal) (*managev1.ContentUpdatedEvent, error) {
	entityTypes := map[string]managev1.ContentEntityType{
		"post":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST,
		"page":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
		"work":          managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK,
		"program_event": managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT,
		"release":       managev1.ContentEntityType_CONTENT_ENTITY_TYPE_RELEASE,
	}
	entityType, ok := entityTypes[relation.ResourceType]
	if !ok || relation.ResourceID == "" {
		return nil, fmt.Errorf("unsupported File download policy signal owner %s", relation.ResourceType)
	}
	return &managev1.ContentUpdatedEvent{
		EntityType: entityType, EntityId: relation.ResourceID,
		Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_MANAGE,
		DocumentStateChanged: false, TimestampMs: relation.TimestampMS,
		ChangedFields: []*managev1.ContentUpdatedField{{
			Path: "file_download_policy", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_CONFIGURATION,
		}},
	}, nil
}
