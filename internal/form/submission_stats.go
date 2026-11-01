package form

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// GetFormSubmissionStatsQuery reads the submission statistics of a Form.
type GetFormSubmissionStatsQuery struct {
	FormID string
}

// SubmissionStats aggregates the submissions of a Form.
type SubmissionStats struct {
	TotalSubmissions     int32
	SubmissionsToday     int32
	SubmissionsThisWeek  int32
	SubmissionsThisMonth int32
	FieldStats           []FieldStat
}

// FieldStat is the ten most frequent answers of one field.
type FieldStat struct {
	FieldID    string
	FieldLabel string
	Values     []FieldValue
}

// FieldValue is one answer and how often it was given.
type FieldValue struct {
	Value string
	Count int32
}

// GetFormSubmissionStats aggregates the submissions of a Form the caller
// manages and records the personal data access.
func (s *Service) GetFormSubmissionStats(ctx context.Context, query GetFormSubmissionStatsQuery) (SubmissionStats, error) {
	formID := strings.TrimSpace(query.FormID)
	if formID == "" {
		return SubmissionStats{}, domainerr.Required("form_id")
	}
	if err := s.requireExistingFormAction(ctx, formID, formActionManage, domainerr.ResourceNotFound("form", formID)); err != nil {
		return SubmissionStats{}, err
	}
	var formSchema []byte
	var submissions []SubmissionData
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		if formSchema, _, err = loadFormSourceSchema(ctx, store, formID); err != nil {
			return err
		}
		submissions, err = store.SubmissionData(ctx, formID)
		return err
	}); err != nil {
		return SubmissionStats{}, err
	}
	stats := buildFormSubmissionStats(ctx, formSchema, submissions, time.Now())
	if err := s.personalDataAccess.AppendFormSubmissions(ctx, formID); err != nil {
		return SubmissionStats{}, securityaccess.Unavailable().WithCause(err)
	}
	return stats, nil
}

func buildFormSubmissionStats(
	ctx context.Context,
	formSchema []byte,
	submissions []SubmissionData,
	now time.Time,
) SubmissionStats {
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

	fieldStats := make([]FieldStat, 0, len(fieldValues))
	for fieldID, valueCounts := range fieldValues {
		fieldLabel := fieldLabels[fieldID]
		if strings.TrimSpace(fieldLabel) == "" {
			fieldLabel = fieldID
		}

		values := make([]FieldValue, 0, len(valueCounts))
		for value, count := range valueCounts {
			values = append(values, FieldValue{
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

		fieldStats = append(fieldStats, FieldStat{
			FieldID:    fieldID,
			FieldLabel: fieldLabel,
			Values:     values,
		})
	}

	sort.Slice(fieldStats, func(i, j int) bool {
		return fieldStats[i].FieldID < fieldStats[j].FieldID
	})

	return SubmissionStats{
		TotalSubmissions:     int32(len(submissions)),
		SubmissionsToday:     submissionsToday,
		SubmissionsThisWeek:  submissionsThisWeek,
		SubmissionsThisMonth: submissionsThisMonth,
		FieldStats:           fieldStats,
	}
}

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
