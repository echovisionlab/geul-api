package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

var criteriaSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"kind":       "kind",
	},
	DefaultSort: "name ASC, id ASC",
}

var criteriaFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name"},
		},
	},
}

var authenticatedAccessKinds = []model.MemberCriteriaKind{
	model.MemberCriteriaKindMemberTags,
	model.MemberCriteriaKindFilter,
}

func (s store) ListCriteriaSet(ctx context.Context, includeArchived bool, page membercriteria.Page) ([]membercriteria.Criteria, int64, error) {
	list := s.tx.WithContext(ctx).Model(&model.MemberCriteria{})
	if !includeArchived {
		list = list.Where("archived_at IS NULL")
	}
	rows, total, err := listPage(list, page)
	if err != nil {
		return nil, 0, err
	}
	pointers := make([]*model.MemberCriteria, 0, len(rows))
	for i := range rows {
		pointers = append(pointers, &rows[i])
	}
	if err := LoadCriteriaConfigs(ctx, s.tx, pointers); err != nil {
		return nil, 0, err
	}
	return criteriaSetOf(rows), total, nil
}

func (s store) ListAuthenticatedAccessCriteriaSet(ctx context.Context, page membercriteria.Page) ([]membercriteria.Criteria, int64, error) {
	list := s.tx.WithContext(ctx).
		Model(&model.MemberCriteria{}).
		Where("kind IN ?", authenticatedAccessKinds).
		Where("archived_at IS NULL")
	rows, total, err := listPage(list, page)
	if err != nil {
		return nil, 0, err
	}
	return criteriaSetOf(rows), total, nil
}

// listPage counts what the page's filters select and reads the page in its
// sort order.
func listPage(list *gorm.DB, page membercriteria.Page) ([]model.MemberCriteria, int64, error) {
	list, err := criteriaFilterConfig.ApplyFilters(list, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list, err = criteriaSortConfig.ApplySort(list, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.MemberCriteria
	if err := list.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func criteriaSetOf(rows []model.MemberCriteria) []membercriteria.Criteria {
	criteriaSet := make([]membercriteria.Criteria, len(rows))
	for i, row := range rows {
		criteriaSet[i] = CriteriaOf(row)
	}
	return criteriaSet
}

func (s store) CriteriaReferences(ctx context.Context, ids []string) (map[string]membercriteria.References, error) {
	references := make(map[string]membercriteria.References, len(ids))
	if len(ids) == 0 {
		return references, nil
	}
	campaigns, runs, err := s.campaigns.CountCriteriaUsage(ctx, s.tx, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range campaigns {
		counted := references[id]
		counted.Campaigns = count
		references[id] = counted
	}
	for id, count := range runs {
		counted := references[id]
		counted.DeliveryRuns = count
		references[id] = counted
	}
	var rows []struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT member_criteria_id AS id, COUNT(*) AS count
		FROM (
			SELECT member_criteria_id
			FROM content_block_attachment_download_member_criteria
			WHERE member_criteria_id IN ?
			UNION ALL
			SELECT member_criteria_id
			FROM track_download_member_criteria
			WHERE member_criteria_id IN ?
		) AS policy_reference
		GROUP BY member_criteria_id
	`, ids, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counted := references[row.ID]
		counted.DownloadPolicies = int32(row.Count)
		references[row.ID] = counted
	}
	signInApps, err := s.signInApps.CountSignInApps(ctx, s.tx, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range signInApps {
		counted := references[id]
		counted.SignInApps = count
		references[id] = counted
	}
	return references, nil
}

// ValidateAuthenticatedAccessCriteriaIDs locks the Member criteria a download policy
// names, in the order relation-policy mutations lock them, and validates that
// each one exists and can grant authenticated access. File's policy
// mutations call it in their own transaction, before they lock the exact
// attachment or Track.
func ValidateAuthenticatedAccessCriteriaIDs(ctx context.Context, db *gorm.DB, criteriaIDs []string) error {
	if len(criteriaIDs) == 0 {
		return nil
	}
	var rows []model.MemberCriteria
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id IN ?", criteriaIDs).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("lock authenticated Member criteria: %w", err)
	}
	return membercriteria.RequireAuthenticatedAccessCriteriaSet(criteriaIDs, criteriaSetOf(rows))
}
