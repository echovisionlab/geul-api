package translation

import (
	"reflect"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestTargetCatalogIsCompleteAndBidirectional(t *testing.T) {
	t.Parallel()
	expected := []Definition{
		{KindPost, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST, "post", "post_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST},
		{KindPage, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PAGE, "page", "page_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE},
		{KindWork, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_WORK, "work", "work_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK},
		{KindProgramEvent, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PROGRAM_EVENT,
			"program_event", "program_event_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT},
		{KindMenu, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_MENU, "menu", "menu_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU},
		{KindEmailTemplate, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_EMAIL_TEMPLATE,
			"email_template", "email_template_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE},
		{KindEmailLayout, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_EMAIL_LAYOUT,
			"email_layout", "email_layout_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_LAYOUT},
		{KindCampaign, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_CAMPAIGN,
			"campaign", "campaign_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN},
		{KindForm, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_FORM, "form", "form_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM},
		{KindPrivacy, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PRIVACY,
			"privacy_history", "privacy_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY},
		{KindTerms, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_TERMS,
			"terms_history", "terms_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS},
		{KindPostSeries, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST_SERIES,
			"series", "series_translation",
			managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES},
	}
	if !reflect.DeepEqual(definitions, expected) {
		t.Fatalf("target catalog = %#v, want exact mappings %#v", definitions, expected)
	}
	for _, definition := range definitions {
		byKind, ok := DefinitionForKind(string(definition.Kind))
		if !ok || byKind != definition {
			t.Fatalf("DefinitionForKind(%q) = %#v, %t", definition.Kind, byKind, ok)
		}
		byProto, ok := DefinitionForProto(definition.Proto)
		if !ok || byProto != definition {
			t.Fatalf("DefinitionForProto(%v) = %#v, %t", definition.Proto, byProto, ok)
		}
	}
}

func TestTargetCatalogExcludesSiteSettings(t *testing.T) {
	t.Parallel()
	if _, ok := DefinitionForKind("site_setting"); ok {
		t.Fatal("Site Settings must not be a Translation target")
	}
}
