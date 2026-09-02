package mcp

import (
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type translationTargetArguments struct {
	Profile  core.Domain            `json:"p"`
	Document core.DocumentReference `json:"d"`
}

type translationGetArguments struct {
	translationTargetArguments
	Locale core.Locale `json:"l"`
}

type translationJobArguments struct {
	JobID string `json:"j"`
}

type translationRegenerateArguments struct {
	translationTargetArguments
	Locales []core.Locale `json:"l"`
}

type translationJobsListArguments struct {
	Profile      *core.Domain            `json:"p,omitempty"`
	Document     *core.DocumentReference `json:"d,omitempty"`
	TargetLocale *core.Locale            `json:"tl,omitempty"`
	SourceLocale *core.Locale            `json:"sl,omitempty"`
	Statuses     []string                `json:"s,omitempty"`
	Limit        int32                   `json:"n,omitempty"`
	Offset       int32                   `json:"o,omitempty"`
	Sort         string                  `json:"k,omitempty"`
	Descending   bool                    `json:"z,omitempty"`
}

func translationLocales(input []core.Locale) ([]string, error) {
	if len(input) == 0 {
		return nil, errors.New("at least one explicit target locale is required")
	}
	locales := make([]string, 0, len(input))
	seen := make(map[core.Locale]struct{}, len(input))
	for _, locale := range input {
		if err := validateCompactLocale(locale); err != nil {
			return nil, err
		}
		if _, duplicate := seen[locale]; duplicate {
			return nil, fmt.Errorf("target locale %q is repeated", locale)
		}
		seen[locale] = struct{}{}
		locales = append(locales, string(locale))
	}
	return locales, nil
}

func translationTarget(input translationTargetArguments) (*managev1.TranslationTarget, error) {
	entityType, ok := translationEntityType(input.Profile)
	if !ok {
		return nil, fmt.Errorf("unsupported translation document profile %q", input.Profile)
	}
	if err := validateCompactOpaque("document reference", string(input.Document), 256); err != nil {
		return nil, err
	}
	return &managev1.TranslationTarget{EntityType: entityType, EntityId: string(input.Document)}, nil
}

var translationEntityTypes = map[core.Domain]managev1.TranslationEntityType{
	core.DomainPage:          managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PAGE,
	core.DomainPost:          managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST,
	core.DomainWork:          managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_WORK,
	core.DomainMenu:          managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_MENU,
	core.DomainEmailTemplate: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_EMAIL_TEMPLATE,
	core.DomainEmailLayout:   managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_EMAIL_LAYOUT,
	core.DomainPrivacy:       managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PRIVACY,
	core.DomainTerms:         managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_TERMS,
	core.DomainCampaign:      managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_CAMPAIGN,
	core.DomainForm:          managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_FORM,
	core.DomainProgramEvent:  managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PROGRAM_EVENT,
	core.DomainPostSeries:    managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST_SERIES,
}

var translationProfiles = func() map[managev1.TranslationEntityType]core.Domain {
	profiles := make(map[managev1.TranslationEntityType]core.Domain, len(translationEntityTypes))
	for profile, entityType := range translationEntityTypes {
		profiles[entityType] = profile
	}
	return profiles
}()

func translationEntityType(profile core.Domain) (managev1.TranslationEntityType, bool) {
	value, ok := translationEntityTypes[profile]
	return value, ok
}

func translationProfile(entityType managev1.TranslationEntityType) (core.Domain, bool) {
	value, ok := translationProfiles[entityType]
	return value, ok
}

func translationJobsListRequest(input translationJobsListArguments) (*managev1.ListTranslationJobsRequest, error) {
	if input.Limit < 0 || input.Limit > 100 {
		return nil, errors.New("translation Job list limit must be between 0 and 100")
	}
	if input.Offset < 0 {
		return nil, errors.New("translation Job list offset cannot be negative")
	}
	request := &managev1.ListTranslationJobsRequest{Pagination: &commonv1.PaginationRequest{Limit: input.Limit, Offset: input.Offset}}
	if (input.Profile == nil) != (input.Document == nil) {
		return nil, errors.New("job list document profile and reference must be provided together")
	}
	if input.Profile != nil {
		target, err := translationTarget(translationTargetArguments{Profile: *input.Profile, Document: *input.Document})
		if err != nil {
			return nil, err
		}
		request.Filters = append(request.Filters,
			&commonv1.FilterSpec{Field: "entity_type", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: translationStorageEntityType(target.EntityType)},
			&commonv1.FilterSpec{Field: "entity_id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: target.EntityId},
		)
	}
	for _, filter := range []struct {
		field string
		value *core.Locale
	}{{"target_locale", input.TargetLocale}, {"source_locale", input.SourceLocale}} {
		if filter.value == nil {
			continue
		}
		if err := validateCompactLocale(*filter.value); err != nil {
			return nil, err
		}
		request.Filters = append(request.Filters, &commonv1.FilterSpec{Field: filter.field, Op: commonv1.FilterOp_FILTER_OP_EQ, Value: string(*filter.value)})
	}
	if len(input.Statuses) != 0 {
		seen := make(map[string]struct{}, len(input.Statuses))
		for _, status := range input.Statuses {
			if _, ok := translationJobStatusesByName[status]; !ok {
				return nil, fmt.Errorf("unsupported translation Job status %q", status)
			}
			if _, duplicate := seen[status]; duplicate {
				return nil, fmt.Errorf("translation Job status %q is repeated", status)
			}
			seen[status] = struct{}{}
		}
		request.Filters = append(request.Filters, &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_IN, Values: append([]string(nil), input.Statuses...)})
	}
	if input.Sort != "" {
		if !validTranslationJobSort(input.Sort) {
			return nil, fmt.Errorf("unsupported translation Job sort %q", input.Sort)
		}
		order := commonv1.SortOrder_SORT_ORDER_ASC
		if input.Descending {
			order = commonv1.SortOrder_SORT_ORDER_DESC
		}
		request.Sorts = []*commonv1.SortSpec{{Field: input.Sort, Order: order}}
	} else if input.Descending {
		return nil, errors.New("descending requires an explicit sort field")
	}
	return request, nil
}

func translationStorageEntityType(entityType managev1.TranslationEntityType) string {
	if entityType == managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST_SERIES {
		return "series"
	}
	profile, _ := translationProfile(entityType)
	return string(profile)
}

func validTranslationJobSort(sort string) bool {
	switch sort {
	case "requested_at", "updated_at", "target_locale", "status":
		return true
	default:
		return false
	}
}

func validateCompactLocale(locale core.Locale) error {
	value := string(locale)
	if value == "" || len(value) > 35 {
		return errors.New("locale must contain 1 to 35 characters")
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (character == '-' && index > 0 && index < len(value)-1) {
			continue
		}
		return fmt.Errorf("locale %q is not a canonical locale identifier", locale)
	}
	return nil
}

func validateCompactOpaque(name, value string, maxLength int) error {
	if value == "" || len(value) > maxLength {
		return fmt.Errorf("%s must contain 1 to %d characters", name, maxLength)
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("%s contains whitespace outside its opaque value", name)
	}
	return nil
}
