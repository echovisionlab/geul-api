package work

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

func nullableStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func normalizeStringIDs(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeInitialSourceLocale(ctx context.Context, db *gorm.DB, locale string) string {
	if normalized := localization.NormalizeSupportedLocale(locale); normalized != nil {
		return *normalized
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		if normalized := localization.NormalizeSupportedLocale(settings.DefaultLocale); normalized != nil {
			return *normalized
		}
	}
	return translation.DefaultLocale
}

func resolveInitialSourceLocale(ctx context.Context, db *gorm.DB, _ auth.IdentityManager, acceptLanguage string) string {
	if principal := auth.GetUser(ctx); principal != nil && principal.Authenticated && db != nil {
		var preferred *string
		if err := db.WithContext(ctx).Model(&model.Member{}).
			Select("preferred_locale").
			Where("id = ?::uuid AND deleted_at IS NULL", principal.MemberID.String()).
			Scan(&preferred).Error; err == nil && preferred != nil {
			if locale := localization.NormalizeSupportedLocale(*preferred); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	return normalizeInitialSourceLocale(ctx, db, "")
}

func normalizeOptionalNullableString(value *string) (*string, bool) {
	if value == nil {
		return nil, false
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, true
	}
	return &normalized, true
}

func validateSlugWithoutSlash(slug string) error {
	if strings.Contains(slug, "/") {
		return errs.InvalidArgument("slug", "must not contain '/'")
	}
	return nil
}

// IsValidUUID reports whether value is a UUID accepted by Work public reads.
func IsValidUUID(value string) bool { _, err := uuid.Parse(value); return err == nil }

func loadReadyManageOgAssetRefs(
	ctx context.Context,
	assets MediaAssets,
	db *gorm.DB,
	candidates ...*string,
) (map[string]*commonv1.AssetRef, error) {
	ids := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		id := strings.TrimSpace(*candidate)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	ready := make(map[string]*commonv1.AssetRef, len(ids))
	if len(ids) == 0 {
		return ready, nil
	}
	ready, err := assets.ResolveReadyAssetRefs(ctx, db, ids)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return ready, nil
}

func readyManageOgAssetRef(
	ctx context.Context,
	assets MediaAssets,
	db *gorm.DB,
	candidates ...*string,
) (*commonv1.AssetRef, error) {
	ready, err := loadReadyManageOgAssetRefs(ctx, assets, db, candidates...)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
			return asset, nil
		}
	}
	return nil, nil
}

func manageOgAssetFromReadyMap(ready map[string]*commonv1.AssetRef, candidates ...*string) *commonv1.AssetRef {
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		if asset := ready[strings.TrimSpace(*candidate)]; asset != nil {
			return asset
		}
	}
	return nil
}

func timestampProtoPtr(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}
