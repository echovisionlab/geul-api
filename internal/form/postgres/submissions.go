package formpostgres

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

// SubmissionOf maps a form_submission row to the domain Submission.
func SubmissionOf(row model.FormSubmission) form.Submission {
	return form.Submission{
		ID: row.ID, FormID: row.FormID, MemberID: row.MemberID, Data: row.Data, IPAddress: row.IPAddress,
		CountryCode: row.CountryCode, UserAgent: row.UserAgent, CreatedAt: row.CreatedAt,
	}
}

// InsertSubmission stores a new submission in tx and fills it with the
// stored row; the public read Store submits through it.
func InsertSubmission(ctx context.Context, tx *gorm.DB, submission *form.Submission) error {
	row := model.FormSubmission{
		FormID: submission.FormID, MemberID: submission.MemberID, Data: submission.Data, IPAddress: submission.IPAddress,
		CountryCode: submission.CountryCode, UserAgent: submission.UserAgent, CreatedAt: submission.CreatedAt,
	}
	if err := tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return err
	}
	*submission = SubmissionOf(row)
	return nil
}

func (s *store) ListSubmissions(ctx context.Context, list form.SubmissionList) ([]form.Submission, int64, error) {
	filtered := filterSubmissions(s.tx.WithContext(ctx).Model(&model.FormSubmission{}).Where("form_id = ?", list.FormID), list)
	sorted := submissionOrder(filtered, list.Order)
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.FormSubmission
	if err := sorted.Limit(int(list.Limit)).Offset(int(list.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	submissions := make([]form.Submission, len(rows))
	for i, row := range rows {
		submissions[i] = SubmissionOf(row)
	}
	return submissions, total, nil
}

// filterSubmissions applies the search, country and creation bounds of
// list.
func filterSubmissions(query *gorm.DB, list form.SubmissionList) *gorm.DB {
	if list.Search != "" {
		pattern := "%" + queryutil.EscapeILIKEPattern(list.Search) + "%"
		query = query.Where("data::text ILIKE ? ESCAPE E'\\\\'", pattern)
	}
	if list.CountryCode != "" {
		query = query.Where("country_code = ?", list.CountryCode)
	}
	if list.CreatedAtFrom != nil {
		query = query.Where("created_at >= ?", *list.CreatedAtFrom)
	}
	if list.CreatedAtBefore != nil {
		query = query.Where("created_at < ?", *list.CreatedAtBefore)
	}
	return query
}

func submissionOrder(query *gorm.DB, order []form.SubmissionOrder) *gorm.DB {
	if len(order) == 0 {
		return query.Order("created_at DESC").Order("id DESC")
	}
	for _, sort := range order {
		direction := "ASC"
		if sort.Descending {
			direction = "DESC"
		}
		query = query.Order("created_at " + direction)
	}
	return query.Order("id DESC")
}

func (s *store) SubmissionCounts(ctx context.Context, formIDs []string) (map[string]int64, error) {
	counts := make(map[string]int64, len(formIDs))
	if len(formIDs) == 0 {
		return counts, nil
	}

	var rows []struct {
		FormID string `gorm:"column:form_id"`
		Count  int64  `gorm:"column:submission_count"`
	}
	if err := s.tx.WithContext(ctx).
		Table("form_submission").
		Select("form_id, COUNT(*) AS submission_count").
		Where("form_id IN ?", formIDs).
		Group("form_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counts[row.FormID] = row.Count
	}
	return counts, nil
}

func (s *store) SubmissionData(ctx context.Context, formID string) ([]form.SubmissionData, error) {
	var rows []struct {
		CreatedAt time.Time `gorm:"column:created_at"`
		Data      []byte    `gorm:"column:data"`
	}
	if err := s.tx.WithContext(ctx).
		Table("form_submission").
		Select("created_at, data").
		Where("form_id = ?", formID).
		Order("created_at DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	submissions := make([]form.SubmissionData, len(rows))
	for i, row := range rows {
		submissions[i] = form.SubmissionData{CreatedAt: row.CreatedAt, Data: row.Data}
	}
	return submissions, nil
}

func (s *store) FindSubmission(ctx context.Context, submissionID string) (form.Submission, error) {
	var row model.FormSubmission
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", submissionID).Error; err != nil {
		return form.Submission{}, notFound(err, form.ErrSubmissionNotFound)
	}
	return SubmissionOf(row), nil
}

func (s *store) DeleteSubmission(ctx context.Context, submissionID string) (bool, error) {
	result := s.tx.WithContext(ctx).Delete(&model.FormSubmission{}, "id = ?", submissionID)
	return result.RowsAffected != 0, result.Error
}
