package menuadapter

import (
	"context"
	"fmt"
	"sort"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TargetReferences resolves Menu target identities against owning read models.
type TargetReferences struct{}

func NewTargetReferences() TargetReferences { return TargetReferences{} }

var _ menudomain.TargetReferences = TargetReferences{}

func (TargetReferences) ValidateAndLock(
	ctx context.Context,
	tx *gorm.DB,
	refs []menudomain.TargetReference,
) error {
	for _, target := range []struct {
		linkType managev1.MenuLinkType
		table    string
		name     string
	}{
		{managev1.MenuLinkType_MENU_LINK_TYPE_PAGE, "page", "page"},
		{managev1.MenuLinkType_MENU_LINK_TYPE_CATEGORY, "category", "category"},
		{managev1.MenuLinkType_MENU_LINK_TYPE_TAG, "tag", "tag"},
		{managev1.MenuLinkType_MENU_LINK_TYPE_SERIES, "series", "series"},
	} {
		if err := validateAndLockTargetKind(ctx, tx, refs, target.linkType, target.table, target.name); err != nil {
			return err
		}
	}
	return nil
}

func validateAndLockTargetKind(
	ctx context.Context,
	tx *gorm.DB,
	refs []menudomain.TargetReference,
	linkType managev1.MenuLinkType,
	table string,
	targetName string,
) error {
	ids, slugs := make([]string, 0), make([]string, 0)
	for _, ref := range refs {
		if ref.LinkType != linkType {
			continue
		}
		if ref.ID != "" {
			ids = append(ids, ref.ID)
		} else if ref.Slug != "" {
			slugs = append(slugs, ref.Slug)
		}
	}
	if len(ids) == 0 && len(slugs) == 0 {
		return nil
	}
	sort.Strings(ids)
	sort.Strings(slugs)

	type targetRow struct {
		ID   string  `gorm:"column:id"`
		Slug *string `gorm:"column:slug"`
	}
	query := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "SHARE"}).
		Table(table).Select("CAST(id AS TEXT) AS id", "slug")
	if len(ids) > 0 && len(slugs) > 0 {
		query = query.Where("CAST(id AS TEXT) IN ? OR slug IN ?", ids, slugs)
	} else if len(ids) > 0 {
		query = query.Where("CAST(id AS TEXT) IN ?", ids)
	} else {
		query = query.Where("slug IN ?", slugs)
	}
	var rows []targetRow
	if err := query.Order("id ASC").Find(&rows).Error; err != nil {
		return errs.Internal(fmt.Errorf("validate menu %s targets: %w", targetName, err))
	}
	foundIDs, foundSlugs := make(map[string]struct{}, len(rows)), make(map[string]struct{}, len(rows))
	for _, row := range rows {
		foundIDs[row.ID] = struct{}{}
		if row.Slug != nil {
			foundSlugs[strings.TrimSpace(*row.Slug)] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, ok := foundIDs[id]; !ok {
			return errs.InvalidArgument("items", targetName+" target does not exist")
		}
	}
	for _, slug := range slugs {
		if _, ok := foundSlugs[slug]; !ok {
			return errs.InvalidArgument("items", targetName+" target does not exist")
		}
	}
	return nil
}
