package programevent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AsyncPublisher is the command and signal surface used by Program Event.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// FileDeleter owns deletion of Files referenced by Program Event lifecycle cleanup.
type FileDeleter interface {
	DeleteFileByID(context.Context, string) error
}

// ContentBlockMediaHydrator adds request-scoped delivery to already-authorized Block references.
type ContentBlockMediaHydrator interface {
	HydrateAuthorizedProgramEventBlockMediaWithDB(
		context.Context,
		*gorm.DB,
		string,
		uuid.UUID,
		*auth.UserInfo,
		[]*contentv1.ContentBlockMediaItem,
	) ([]*contentv1.ContentBlockMediaItem, error)
}

// CreditMemberSummaries projects Member-owned identity details for Program Event credits.
type CreditMemberSummaries interface {
	LoadCreditMemberSummaries(context.Context, []string) (map[string]*commonv1.MemberSummary, error)
}

// MediaAssets is the narrow File/media lifecycle used by Program Event.
// Program Event owns transaction and binding policy; the adapter owns the
// concrete public-asset implementation and CDN projection.
type MediaAssets interface {
	LockAttachableFilesForUpdate(context.Context, *gorm.DB, []string) error
	BindReadyAssetForSourceFile(context.Context, *gorm.DB, string, string, string, string, string) (*commonv1.AssetRef, error)
	ReleasePublicAssetBindings(context.Context, *gorm.DB, string, string, string) error
	ReadyPublicAssetRefForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
	ResolveSingleReadyInlineAssetForSourceFile(context.Context, *gorm.DB, string, string) (*commonv1.AssetRef, error)
}

// Runtime contains the concrete runtime capabilities required by Program Event.
type Runtime interface {
	MediaAssets
}

// CollaborationPermissionChecker is the minimal ReBAC boundary used by Program Event collaboration.
type CollaborationPermissionChecker interface {
	Can(context.Context, policyv1.AuthorizationDecision) (bool, error)
	CheckActorCan(context.Context, policyv1.Actor, policyv1.Can) (bool, error)
}

// RequireExists verifies the Program Event root at cross-domain attachment boundaries.
func RequireExists(ctx context.Context, db *gorm.DB, eventID string) error {
	var event model.ProgramEvent
	if err := db.WithContext(ctx).Select("id").Take(&event, "id = ?", eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("program event", eventID)
		}
		return errs.Internal(err)
	}
	return nil
}

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}

func resolveInitialSourceLocale(ctx context.Context, db *gorm.DB, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated && db != nil {
		var preferredLocale *string
		if err := db.WithContext(ctx).Model(&model.Member{}).
			Select("preferred_locale").
			Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).
			Scan(&preferredLocale).Error; err == nil && preferredLocale != nil {
			if locale := localization.NormalizeSupportedLocale(*preferredLocale); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	}
	return translation.DefaultLocale
}

func lockTranslationEntityRootWithDB(ctx context.Context, db *gorm.DB, entityType, entityID string) error {
	definition, ok := translation.DefinitionForKind(entityType)
	if !ok {
		return errs.InvalidArgument("entity_type", "unsupported translation entity type")
	}
	var root struct {
		ID string `gorm:"column:id"`
	}
	result := db.WithContext(ctx).
		Table(definition.RootTable).
		Select("id").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", entityID).
		Take(&root)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return errs.NotFound(entityType, entityID)
	}
	if result.Error != nil {
		return errs.Internal(result.Error)
	}
	return nil
}

func requireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error {
	if len(contributors) == 0 {
		return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires contributors")
	}
	for index, contributor := range contributors {
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires canonical Member UUIDs")
		}
		if index > 0 && contributors[index-1] >= contributor {
			return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
	}
	var members []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).
		Table("member").
		Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("id::text").
		Where("id IN ?", contributors).
		Find(&members).Error; err != nil {
		return errs.Internal(fmt.Errorf("lock Program Event contributor Members: %w", err))
	}
	if len(members) != len(contributors) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}
