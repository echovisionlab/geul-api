package menupostgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/menu"
	"gorm.io/gorm/clause"
)

// targetTables are the owning-domain tables of each linked target kind.
// Until those domains offer a port, the Menu Store reads their rows.
var targetTables = []struct {
	linkType menu.LinkType
	table    string
}{
	{menu.LinkPage, "page"},
	{menu.LinkCategory, "category"},
	{menu.LinkTag, "tag"},
	{menu.LinkSeries, "series"},
}

func (s store) LockTargets(ctx context.Context, refs []menu.TargetReference) error {
	for _, target := range targetTables {
		if err := s.lockTargetKind(ctx, refs, target.linkType, target.table); err != nil {
			return err
		}
	}
	return nil
}

func (s store) lockTargetKind(
	ctx context.Context,
	refs []menu.TargetReference,
	linkType menu.LinkType,
	table string,
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
	query := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
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
		return fmt.Errorf("validate menu %s targets: %w", table, err)
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
			return menu.TargetNotFoundError{LinkType: linkType}
		}
	}
	for _, slug := range slugs {
		if _, ok := foundSlugs[slug]; !ok {
			return menu.TargetNotFoundError{LinkType: linkType}
		}
	}
	return nil
}
