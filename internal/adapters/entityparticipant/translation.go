// Package entityparticipant adapts shared translation and Member runtime
// services to the participant-governed catalog entities, Artist and Label.
package entityparticipant

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Translation adapts shared translation authority to one entity domain.
type Translation struct {
	entityTitle string
}

// NewTranslation names the entity ("Artist") in internal error text.
func NewTranslation(entityTitle string) *Translation { return &Translation{entityTitle: entityTitle} }

func (*Translation) ResolveInitialSourceLocale(ctx context.Context, db *gorm.DB, acceptLanguage string) string {
	if user := auth.GetUser(ctx); user != nil && user.Authenticated && db != nil {
		var preferred *string
		if err := db.WithContext(ctx).Model(&model.Member{}).Select("preferred_locale").Where("id = ?::uuid AND deleted_at IS NULL", user.MemberID.String()).Scan(&preferred).Error; err != nil {
			slog.ErrorContext(ctx, "Preferred source locale lookup failed; inferring the locale from the request", failurecause.Attr(err))
		} else if preferred != nil {
			if locale := localization.NormalizeSupportedLocale(*preferred); locale != nil {
				return *locale
			}
		}
	}
	if locale := localization.InferPreferredLocaleFromAcceptLanguage(acceptLanguage); locale != nil {
		return *locale
	}
	if settings, err := translationpostgres.NewRuntimeSettingsReader(db).RuntimeSettings(ctx); err != nil {
		slog.ErrorContext(ctx, "Source locale settings load failed; using the default locale", failurecause.Attr(err))
	} else {
		if locale := localization.NormalizeSupportedLocale(settings.DefaultLocale); locale != nil {
			return *locale
		}
	}
	return translation.DefaultLocale
}

func (t *Translation) RequireDocumentContributors(ctx context.Context, tx *gorm.DB, contributors []string) error {
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
		return errs.Internal(fmt.Errorf("lock %s save contributor Members: %w", t.entityTitle, err))
	}
	if len(locked) != len(contributors) {
		return errs.InvalidArgument("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}
