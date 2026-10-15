package contentsignal

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

type interactivePublisherKey struct{}

// WithInteractivePublisher supplies the transactional publisher to domain paths
// whose signals were formerly emitted by the post-commit interactive relay.
func WithInteractivePublisher(ctx context.Context, publisher Publisher) context.Context {
	return context.WithValue(ctx, interactivePublisherKey{}, publisher)
}

func PublishInteractive(ctx context.Context, tx *gorm.DB, entity managev1.ContentEntityType, id, locale, revision string, target *string, changed, deleted bool, member string) error {
	publisher, ok := ctx.Value(interactivePublisherKey{}).(Publisher)
	if !ok || !changed {
		return nil
	}
	exists := !deleted
	return PublishInTransaction(ctx, tx, publisher, &managev1.ContentUpdatedEvent{
		EntityType: entity, EntityId: id, Source: managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_AI,
		DocumentRevision: &revision, TargetRevision: target, Locale: &locale, LocaleExists: &exists,
		DocumentStateChanged: !deleted && target == nil, ContributorMemberIds: []string{member},
		ChangedFields: []*managev1.ContentUpdatedField{{Path: "document.content", Kind: managev1.ContentUpdatedFieldKind_CONTENT_UPDATED_FIELD_KIND_TEXT}},
		TimestampMs:   time.Now().UnixMilli(),
	})
}

func InteractiveMemberID(ctx context.Context) string {
	if user := auth.GetUser(ctx); user != nil {
		return string(user.MemberID)
	}
	return ""
}
