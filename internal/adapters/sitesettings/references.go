package sitesettings

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	domain "github.com/echovisionlab/geul-api/internal/sitesettings"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// References validates Site Settings selections against owning Page and Menu
// read models without moving their lifecycle authority into Site Settings.
type References struct{}

func NewReferences() References { return References{} }

var _ domain.References = References{}

func (References) Validate(ctx context.Context, db *gorm.DB, key string, value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	id := strings.TrimSpace(*value)
	switch key {
	case "homepage_page_id":
		var page model.Page
		if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "SHARE"}).
			Select("id", "status").Where("id = ?", id).Take(&page).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.InvalidArgument(key, "must reference a published page")
			}
			return errs.Internal(err)
		}
		if page.Status != model.PageStatus(managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()) {
			return errs.InvalidArgument(key, "must reference a published page")
		}
		return nil
	case "menu_header_id", "menu_secondary_id", "menu_footer_id", "menu_avatar_dropdown_id":
		var menu model.Menu
		if err := db.WithContext(ctx).Clauses(clause.Locking{Strength: "SHARE"}).
			Select("id").Where("id = ?", id).Take(&menu).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.InvalidArgument(key, "must reference an existing menu")
			}
			return errs.Internal(err)
		}
		return nil
	default:
		return nil
	}
}
