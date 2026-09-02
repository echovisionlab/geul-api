package work

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AsyncPublisher is the transport-neutral capability used for coalescible
// Work content-update signals.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// CollaborationPermissionChecker is the fully-consistent ReBAC read used by
// Work collaboration admission and final persistence fences.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

// ContentBlockMediaHydrator is the public Work media boundary. Public File
// delivery owns its separate share/public policy evaluation.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
}

// AuthorizedContentBlockMediaHydrator adds private editor delivery inside the
// same transaction that owns Work lifecycle and principal authorization.
type AuthorizedContentBlockMediaHydrator interface {
	HydrateAuthorizedWorkBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

type MemberSummaryLoader interface {
	LoadMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

// OGRequests is the narrow OG lifecycle used by Work mutations.
type OGRequests interface {
	RequestCurrentWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string, bool, string) (string, error)
	CancelAndReleaseEntityWithDB(context.Context, *gorm.DB, managev1.OgEntityType, string, string) error
}

// MediaAssets is the narrow File/media projection lifecycle used by Work.
type MediaAssets interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ResolveReadyAssetRefs(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error)
	ReadyPublicAssetRefForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	LoadUnavailableVersionAttachmentKinds(context.Context, *gorm.DB, proto.Message) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error)
}

// Runtime contains only shared OG and media capabilities required by Work.
type Runtime interface {
	OGRequests
	MediaAssets
}

func requireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error {
	if len(contributors) == 0 {
		return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires contributors")
	}
	normalized := make([]string, 0, len(contributors))
	for _, contributor := range contributors {
		contributor = strings.TrimSpace(contributor)
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires canonical Member UUIDs")
		}
		if len(normalized) > 0 && normalized[len(normalized)-1] >= contributor {
			return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
		normalized = append(normalized, contributor)
	}

	var locked []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", normalized).
		Find(&locked).Error; err != nil {
		return errs.Internal(fmt.Errorf("lock Work save contributor Members: %w", err))
	}
	if len(locked) != len(normalized) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

func publishWorkContentUpdated(ctx context.Context, publisher AsyncPublisher, event *managev1.ContentUpdatedEvent) error {
	if publisher == nil || event == nil {
		return nil
	}
	if err := publisher.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, event); err != nil {
		slog.Warn("Failed to publish Work content updated event", "error", err, "workId", event.EntityId)
		return err
	}
	return nil
}
