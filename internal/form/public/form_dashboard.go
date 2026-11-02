package public

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strconv"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// Dashboard is the shared submission dashboard of a Form: its submission
// counts and each field's ten most frequent answers.
type Dashboard struct {
	FormID               string
	FormTitle            string
	FormSlug             string
	TotalSubmissions     int32
	SubmissionsToday     int32
	SubmissionsThisWeek  int32
	SubmissionsThisMonth int32
	FieldStats           []form.FieldStat
}

// GetFormDashboardQuery reads the shared submission dashboard of a Form
// through its dashboard share link.
type GetFormDashboardQuery struct {
	Slug           string
	ShareToken     string
	SharePassword  *string
	AcceptLanguage string
}

// GetFormDashboard returns the dashboard a valid dashboard share link opens;
// without one the Form is not found.
func (s *Service) GetFormDashboard(ctx context.Context, query GetFormDashboardQuery) (Dashboard, error) {
	if query.ShareToken == "" {
		return Dashboard{}, errFormNotFound()
	}
	found, err := s.findFormBySlugOrID(ctx, query.Slug)
	if err != nil {
		return Dashboard{}, err
	}
	shareToken := query.ShareToken
	shareTokenState := s.validateShareToken(
		ctx,
		&found,
		&shareToken,
		query.SharePassword,
		ShareLinkFormDashboard,
	)
	if !shareTokenState.valid {
		return Dashboard{}, errFormNotFound()
	}
	if err := s.enforceFormAccess(ctx, s.reads(), &found, formAccessOptions{
		context:                  ContextURL,
		hasValidPreviewToken:     true,
		bypassAuth:               true,
		bypassRoles:              true,
		draftAsNotFound:          true,
		enforcePassword:          false,
		checkSubmissionLimit:     false,
		checkDuplicateSubmission: false,
	}); err != nil {
		return Dashboard{}, err
	}
	return s.buildFormDashboard(ctx, &found, query.AcceptLanguage)
}

func stringifyDashboardValue(value structured.Value) (string, bool) {
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

func (s *Service) buildFormDashboard(
	ctx context.Context,
	found *form.Form,
	acceptLanguage string,
) (Dashboard, error) {
	sourceDocument, _, err := s.loadResolvedFormDocument(ctx, found, acceptLanguage)
	if err != nil {
		return Dashboard{}, err
	}

	var submissions []form.Submission
	if err := s.read(ctx, func(store Store) error {
		var err error
		submissions, err = store.Submissions(ctx, found.ID)
		return err
	}); err != nil {
		return Dashboard{}, err
	}

	return buildFormDashboardFromSubmissions(ctx, found, sourceDocument, submissions, time.Now()), nil
}

func buildFormDashboardFromSubmissions(
	ctx context.Context,
	found *form.Form,
	sourceDocument *form.FormSourceDocument,
	submissions []form.Submission,
	now time.Time,
) Dashboard {
	fieldLabels := form.ExtractFormFieldLabels(sourceDocument.Schema)
	maps.Copy(fieldLabels, formSubmissionMetaFieldLabels)
	periods := countFormDashboardPeriods(submissions, now)

	formSlug := found.ID
	if found.Slug != nil && *found.Slug != "" {
		formSlug = *found.Slug
	}

	return Dashboard{
		FormID:               found.ID,
		FormTitle:            sourceDocument.Title,
		FormSlug:             formSlug,
		TotalSubmissions:     int32(len(submissions)),
		SubmissionsToday:     periods.today,
		SubmissionsThisWeek:  periods.thisWeek,
		SubmissionsThisMonth: periods.thisMonth,
		FieldStats:           formDashboardFieldStats(fieldLabels, countFormDashboardFieldValues(ctx, submissions)),
	}
}

// formDashboardPeriods counts the submissions created since the start of
// the day, week and month.
type formDashboardPeriods struct {
	today, thisWeek, thisMonth int32
}

func countFormDashboardPeriods(submissions []form.Submission, now time.Time) formDashboardPeriods {
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -int(startOfDay.Weekday()))
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	var periods formDashboardPeriods
	for _, submission := range submissions {
		createdAt := submission.CreatedAt
		if !createdAt.Before(startOfDay) {
			periods.today++
		}
		if !createdAt.Before(startOfWeek) {
			periods.thisWeek++
		}
		if !createdAt.Before(startOfMonth) {
			periods.thisMonth++
		}
	}
	return periods
}

// countFormDashboardFieldValues counts each scalar value of each submitted
// field.
func countFormDashboardFieldValues(ctx context.Context, submissions []form.Submission) map[string]map[string]int32 {
	fieldValues := make(map[string]map[string]int32)
	for _, submission := range submissions {
		var data structured.Fields
		if err := json.Unmarshal(submission.Data, &data); err != nil {
			slog.ErrorContext(ctx, "Stored form submission is not valid JSON; skipping it in the dashboard", "domain", "form", "event", "form.operation_failed", "operation", "decode_dashboard_submission", "outcome", "failed", failurecause.Attr(err))
		}
		for fieldID, rawValue := range data {
			value, ok := stringifyDashboardValue(rawValue)
			if !ok {
				continue
			}
			if _, exists := fieldValues[fieldID]; !exists {
				fieldValues[fieldID] = make(map[string]int32)
			}
			fieldValues[fieldID][value]++
		}
	}
	return fieldValues
}

// formDashboardFieldStats lists each field's ten most frequent values,
// fields ordered by ID.
func formDashboardFieldStats(fieldLabels map[string]string, fieldValues map[string]map[string]int32) []form.FieldStat {
	fieldStats := make([]form.FieldStat, 0, len(fieldValues))
	for fieldID, valueCounts := range fieldValues {
		fieldLabel := fieldLabels[fieldID]
		if fieldLabel == "" {
			fieldLabel = fieldID
		}
		values := make([]form.FieldValue, 0, len(valueCounts))
		for value, count := range valueCounts {
			values = append(values, form.FieldValue{Value: value, Count: count})
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
		fieldStats = append(fieldStats, form.FieldStat{FieldID: fieldID, FieldLabel: fieldLabel, Values: values})
	}
	sort.Slice(fieldStats, func(i, j int) bool {
		return fieldStats[i].FieldID < fieldStats[j].FieldID
	})
	return fieldStats
}
