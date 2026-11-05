package mcp

import (
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
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
	core.DomainArtist:        managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_ARTIST,
	core.DomainLabel:         managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_LABEL,
	core.DomainRelease:       managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_RELEASE,
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

func validateCompactLocale(locale core.Locale) error {
	value := string(locale)
	if value == "" || len(value) > 35 {
		return errors.New("locale must contain 1 to 35 characters")
	}
	for _, subtag := range strings.Split(value, "-") {
		if subtag == "" {
			return fmt.Errorf("locale %q requires non-empty alphanumeric subtags", locale)
		}
		for _, character := range subtag {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') {
				continue
			}
			return fmt.Errorf("locale %q requires non-empty alphanumeric subtags", locale)
		}
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
