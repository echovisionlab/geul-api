package public

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"time"

	"connectrpc.com/connect"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

// FormService implements the public FormService
func (s *FormService) GetDashboard(
	ctx context.Context,
	req *connect.Request[openv1.GetFormDashboardRequest],
) (*connect.Response[openv1.GetFormDashboardResponse], error) {
	if req.Msg.ShareToken == "" {
		return nil, errs.NotFoundMsg("form not found")
	}

	form, err := s.findFormBySlugOrID(ctx, req.Msg.Slug)
	if err != nil {
		return nil, err
	}

	shareToken := req.Msg.ShareToken
	shareTokenState := s.validateShareToken(
		ctx,
		form,
		&shareToken,
		req.Msg.SharePassword,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD,
	)
	if !shareTokenState.valid {
		return nil, errs.NotFoundMsg("form not found")
	}

	if err := s.enforceFormAccess(ctx, form, formAccessOptions{
		context:                  openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL,
		hasValidPreviewToken:     true,
		bypassAuth:               true,
		bypassRoles:              true,
		draftAsNotFound:          true,
		enforcePassword:          false,
		checkSubmissionLimit:     false,
		checkDuplicateSubmission: false,
	}); err != nil {
		return nil, err
	}

	dashboard, err := s.buildFormDashboard(ctx, form, req.Header().Get("Accept-Language"))
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&openv1.GetFormDashboardResponse{
		Dashboard: dashboard,
	}), nil
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

func (s *FormService) buildFormDashboard(
	ctx context.Context,
	form *model.Form,
	acceptLanguage string,
) (*openv1.FormDashboard, error) {
	sourceDocument, err := s.loadResolvedFormDocument(ctx, form, acceptLanguage)
	if err != nil {
		return nil, err
	}

	var submissions []model.FormSubmission
	if err := s.db.WithContext(ctx).
		Where("form_id = ?", form.ID).
		Order("created_at DESC").
		Find(&submissions).Error; err != nil {
		return nil, errs.Internal(err)
	}

	return buildFormDashboardFromSubmissions(form, sourceDocument, submissions, time.Now()), nil
}

func buildFormDashboardFromSubmissions(
	form *model.Form,
	sourceDocument *formdomain.FormSourceDocument,
	submissions []model.FormSubmission,
	now time.Time,
) *openv1.FormDashboard {
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -int(startOfDay.Weekday()))
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	var submissionsToday int32
	var submissionsThisWeek int32
	var submissionsThisMonth int32
	fieldValues := make(map[string]map[string]int32)
	fieldLabels := formdomain.ExtractFormFieldLabels(sourceDocument.Schema)
	maps.Copy(fieldLabels, formSubmissionMetaFieldLabels)

	for _, submission := range submissions {
		createdAt := submission.CreatedAt
		if createdAt.After(startOfDay) || createdAt.Equal(startOfDay) {
			submissionsToday++
		}
		if createdAt.After(startOfWeek) || createdAt.Equal(startOfWeek) {
			submissionsThisWeek++
		}
		if createdAt.After(startOfMonth) || createdAt.Equal(startOfMonth) {
			submissionsThisMonth++
		}

		var data structured.Fields
		_ = json.Unmarshal(submission.Data, &data)

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

	fieldStats := make([]*openv1.FormDashboardFieldStat, 0, len(fieldValues))
	for fieldID, valueCounts := range fieldValues {
		fieldLabel := fieldLabels[fieldID]
		if fieldLabel == "" {
			fieldLabel = fieldID
		}

		values := make([]*openv1.FormDashboardFieldValue, 0, len(valueCounts))
		for value, count := range valueCounts {
			values = append(values, &openv1.FormDashboardFieldValue{
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

		fieldStats = append(fieldStats, &openv1.FormDashboardFieldStat{
			FieldId:    fieldID,
			FieldLabel: fieldLabel,
			Values:     values,
		})
	}

	sort.Slice(fieldStats, func(i, j int) bool {
		return fieldStats[i].FieldId < fieldStats[j].FieldId
	})

	formSlug := form.ID
	if form.Slug != nil && *form.Slug != "" {
		formSlug = *form.Slug
	}

	return &openv1.FormDashboard{
		FormId:               form.ID,
		FormTitle:            sourceDocument.Title,
		FormSlug:             formSlug,
		TotalSubmissions:     int32(len(submissions)),
		SubmissionsToday:     submissionsToday,
		SubmissionsThisWeek:  submissionsThisWeek,
		SubmissionsThisMonth: submissionsThisMonth,
		FieldStats:           fieldStats,
	}
}

// VerifyPassword checks if the provided password is correct for a form
