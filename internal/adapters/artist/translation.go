package artist

import (
	"context"
	"fmt"
	"strings"

	artistdomain "github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Translation struct{}

func NewTranslation() *Translation { return &Translation{} }

func (*Translation) ResolveInitialSourceLocale(ctx context.Context, db *gorm.DB, identity auth.IdentityManager, language string) string {
	_ = identity
	if user := auth.GetUser(ctx); user != nil && user.Authenticated && db != nil {
		var preferred *string
		if err := db.WithContext(ctx).Model(&model.Member{}).Select("preferred_locale").Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).Scan(&preferred).Error; err == nil && preferred != nil {
			if locale := localization.NormalizeSupportedLocale(*preferred); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(language); locale != nil {
		return *locale
	}
	if settings, err := translation.LoadRuntimeSettings(ctx, db); err == nil {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	}
	return translation.DefaultLocale
}

func (*Translation) NormalizeInitialSourceLocale(ctx context.Context, db *gorm.DB, locale string) string {
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

func (*Translation) RequireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error {
	if len(contributors) == 0 {
		return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires contributors")
	}
	for index, contributor := range contributors {
		if strings.TrimSpace(contributor) != contributor {
			return errs.InvalidArgument("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return errs.InvalidArgument("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		if index > 0 && contributors[index-1] >= contributor {
			return errs.InvalidArgument("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
	}
	var locked []struct {
		ID string `gorm:"column:id"`
	}
	if err := tx.WithContext(ctx).Table("member").Clauses(clause.Locking{Strength: "KEY SHARE"}).Select("id::text").Where("id IN ?", contributors).Find(&locked).Error; err != nil {
		return errs.Internal(fmt.Errorf("lock Artist save contributor Members: %w", err))
	}
	if len(locked) != len(contributors) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}

var _ artistdomain.Translation = (*Translation)(nil)
