package form

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// GetFormSubmissionStatsQuery reads the submission statistics of a Form.
type GetFormSubmissionStatsQuery struct {
	FormID string
}

// GetFormSubmissionStats aggregates the submissions of a Form the caller
// manages and records the personal data access.
func (s *Service) GetFormSubmissionStats(ctx context.Context, query GetFormSubmissionStatsQuery) (*managev1.GetFormSubmissionStatsResponse, error) {
	formID := strings.TrimSpace(query.FormID)
	if formID == "" {
		return nil, domainerr.Required("form_id")
	}
	var form model.Form
	notFound := domainerr.ResourceNotFound("form", formID)
	if err := s.db.WithContext(ctx).Select("id").First(&form, "id = ?", formID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, notFound
		}
		return nil, err
	}
	if err := s.requireVisibleFormAction(ctx, formID, formActionManage, notFound); err != nil {
		return nil, err
	}
	formSchema, _, err := loadFormSourceSchema(ctx, s.db, formID)
	if err != nil {
		return nil, err
	}
	var submissions []formSubmissionStatsRow
	if err := s.db.WithContext(ctx).
		Table("form_submission").
		Select("created_at, data").
		Where("form_id = ?", formID).
		Order("created_at DESC").
		Find(&submissions).Error; err != nil {
		return nil, err
	}
	stats := buildFormSubmissionStats(ctx, formSchema, submissions, time.Now())
	if err := s.personalDataAccess.AppendFormSubmissions(ctx, formID); err != nil {
		return nil, securityaccess.Unavailable().WithCause(err)
	}
	return &managev1.GetFormSubmissionStatsResponse{Stats: stats}, nil
}

type formSubmissionStatsRow struct {
	CreatedAt time.Time `gorm:"column:created_at"`
	Data      []byte    `gorm:"column:data"`
}

func buildFormSubmissionStats(
	ctx context.Context,
	formSchema []byte,
	submissions []formSubmissionStatsRow,
	now time.Time,
) *managev1.FormSubmissionStats {
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -int(startOfDay.Weekday()))
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	var submissionsToday int32
	var submissionsThisWeek int32
	var submissionsThisMonth int32
	fieldValues := make(map[string]map[string]int32)
	fieldLabels := extractFormFieldLabels(formSchema)

	for _, submission := range submissions {
		createdAt := submission.CreatedAt
		if !createdAt.Before(startOfDay) {
			submissionsToday++
		}
		if !createdAt.Before(startOfWeek) {
			submissionsThisWeek++
		}
		if !createdAt.Before(startOfMonth) {
			submissionsThisMonth++
		}

		var data structured.Fields
		if err := json.Unmarshal(submission.Data, &data); err != nil {
			slog.ErrorContext(ctx, "Stored form submission is not valid JSON; skipping it in stats", "domain", "form", "event", "form.operation_failed", "operation", "decode_submission_stats", "outcome", "failed", failurecause.Attr(err))
			continue
		}

		for fieldID, rawValue := range data {
			value, ok := stringifySubmissionStatValue(rawValue)
			if !ok {
				continue
			}
			if _, exists := fieldValues[fieldID]; !exists {
				fieldValues[fieldID] = make(map[string]int32)
			}
			fieldValues[fieldID][value]++
		}
	}

	fieldStats := make([]*managev1.FormSubmissionFieldStat, 0, len(fieldValues))
	for fieldID, valueCounts := range fieldValues {
		fieldLabel := fieldLabels[fieldID]
		if strings.TrimSpace(fieldLabel) == "" {
			fieldLabel = fieldID
		}

		values := make([]*managev1.FormSubmissionFieldValue, 0, len(valueCounts))
		for value, count := range valueCounts {
			values = append(values, &managev1.FormSubmissionFieldValue{
				Value: value,
				Count: count,
			})
		}

		sort.Slice(values, func(i, j int) bool {
			if values[i].Count == values[j].Count {
				return values[i].Value < values[j].Value
			}
			return values[i].Count > values[j].Count
		})
		if len(values) > 10 {
			values = values[:10]
		}

		fieldStats = append(fieldStats, &managev1.FormSubmissionFieldStat{
			FieldId:    fieldID,
			FieldLabel: fieldLabel,
			Values:     values,
		})
	}

	sort.Slice(fieldStats, func(i, j int) bool {
		return fieldStats[i].FieldId < fieldStats[j].FieldId
	})

	return &managev1.FormSubmissionStats{
		TotalSubmissions:     int32(len(submissions)),
		SubmissionsToday:     submissionsToday,
		SubmissionsThisWeek:  submissionsThisWeek,
		SubmissionsThisMonth: submissionsThisMonth,
		FieldStats:           fieldStats,
	}
}

// GetFormSubmissionWithSchema retrieves a submission with its form schema (admin only)
// This combines GetFormSubmission and GetForm into a single call for efficiency
func stringifySubmissionStatValue(value structured.Value) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, true
	case float64:
		return fmt.Sprintf("%v", v), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}
