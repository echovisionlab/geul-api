package translation

import (
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Kind is the stable application identity of one Translation target type.
type Kind string

const (
	KindPage          Kind = "page"
	KindPost          Kind = "post"
	KindWork          Kind = "work"
	KindMenu          Kind = "menu"
	KindEmailTemplate Kind = "email_template"
	KindEmailLayout   Kind = "email_layout"
	KindPrivacy       Kind = "privacy"
	KindTerms         Kind = "terms"
	KindCampaign      Kind = "campaign"
	KindForm          Kind = "form"
	KindProgramEvent  Kind = "program_event"
	KindPostSeries    Kind = "series"
)

// Definition is the common identity and persistence vocabulary for one
// Translation target. Domain-specific authorization and content semantics do
// not belong in this catalog.
type Definition struct {
	Kind              Kind
	Proto             managev1.TranslationEntityType
	RootTable         string
	EntryTable        string
	ContentEntityType managev1.ContentEntityType
}

var definitions = mustDefinitions([]Definition{
	definition(KindPost, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST, "post", "post_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST),
	definition(KindPage, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PAGE, "page", "page_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE),
	definition(KindWork, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_WORK, "work", "work_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_WORK),
	definition(KindProgramEvent, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PROGRAM_EVENT,
		"program_event", "program_event_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PROGRAM_EVENT),
	definition(KindMenu, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_MENU,
		"menu", "menu_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_MENU),
	definition(KindEmailTemplate, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_EMAIL_TEMPLATE,
		"email_template", "email_template_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE),
	definition(KindEmailLayout, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_EMAIL_LAYOUT,
		"email_layout", "email_layout_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_LAYOUT),
	definition(KindCampaign, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_CAMPAIGN,
		"campaign", "campaign_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_CAMPAIGN),
	definition(KindForm, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_FORM,
		"form", "form_translation", managev1.ContentEntityType_CONTENT_ENTITY_TYPE_FORM),
	definition(KindPrivacy, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_PRIVACY,
		"privacy_history", "privacy_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PRIVACY),
	definition(KindTerms, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_TERMS,
		"terms_history", "terms_translation",
		managev1.ContentEntityType_CONTENT_ENTITY_TYPE_TERMS),
	definition(KindPostSeries, managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST_SERIES,
		"series", "series_translation", managev1.ContentEntityType_CONTENT_ENTITY_TYPE_POST_SERIES),
})

var (
	definitionByKind  = definitionsByKind(definitions)
	definitionByProto = definitionsByProto(definitions)
)

// Definitions returns a copy of the complete target catalog in stable product
// order. Callers may sort their copy for storage scans without mutating the
// package authority.
func Definitions() []Definition {
	return append([]Definition(nil), definitions...)
}

// DefinitionForKind resolves an application target identity.
func DefinitionForKind(kind string) (Definition, bool) {
	definition, ok := definitionByKind[Kind(kind)]
	return definition, ok
}

// DefinitionForProto resolves a public wire target identity.
func DefinitionForProto(entityType managev1.TranslationEntityType) (Definition, bool) {
	definition, ok := definitionByProto[entityType]
	return definition, ok
}

func definition(
	kind Kind,
	proto managev1.TranslationEntityType,
	rootTable string,
	entryTable string,
	contentEntityType managev1.ContentEntityType,
) Definition {
	return Definition{
		Kind: kind, Proto: proto, RootTable: rootTable, EntryTable: entryTable,
		ContentEntityType: contentEntityType,
	}
}

func mustDefinitions(catalog []Definition) []Definition {
	byKind := make(map[Kind]struct{}, len(catalog))
	byProto := make(map[managev1.TranslationEntityType]struct{}, len(catalog))
	byContent := make(map[managev1.ContentEntityType]struct{}, len(catalog))
	for _, item := range catalog {
		if item.Kind == "" || item.Proto == managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_UNSPECIFIED ||
			item.RootTable == "" || item.EntryTable == "" {
			panic("translation target definition is incomplete")
		}
		if _, duplicate := byKind[item.Kind]; duplicate {
			panic("duplicate translation target kind " + string(item.Kind))
		}
		byKind[item.Kind] = struct{}{}
		if _, duplicate := byProto[item.Proto]; duplicate {
			panic("duplicate translation target proto")
		}
		byProto[item.Proto] = struct{}{}
		if item.ContentEntityType != managev1.ContentEntityType_CONTENT_ENTITY_TYPE_UNSPECIFIED {
			if _, duplicate := byContent[item.ContentEntityType]; duplicate {
				panic("duplicate translation content entity type")
			}
			byContent[item.ContentEntityType] = struct{}{}
		}
	}
	return append([]Definition(nil), catalog...)
}

func definitionsByKind(catalog []Definition) map[Kind]Definition {
	index := make(map[Kind]Definition, len(catalog))
	for _, definition := range catalog {
		index[definition.Kind] = definition
	}
	return index
}

func definitionsByProto(catalog []Definition) map[managev1.TranslationEntityType]Definition {
	index := make(map[managev1.TranslationEntityType]Definition, len(catalog))
	for _, definition := range catalog {
		index[definition.Proto] = definition
	}
	return index
}
