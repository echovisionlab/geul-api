package application

import (
	"github.com/echovisionlab/geul-api/internal/translation"
	"strings"

	"github.com/echovisionlab/geul-api/internal/localization"
)

func buildDefaultTranslationGenerationProfile(
	jobEntityType string,
	sourceLocale string,
	targetLocale string,
	preserveMarkup bool,
	protectedTerms []string,
) translation.GenerationProfile {
	contentKind := inferTranslationContentKind(jobEntityType)
	registerPolicy := inferTranslationRegisterPolicy(sourceLocale)
	targetRegister := inferTargetTranslationRegister(contentKind, targetLocale)

	profile := translation.GenerationProfile{
		QualityTier:       translation.QualityTierHigh,
		PreserveMarkup:    preserveMarkup,
		ContentKind:       contentKind,
		SourceLocale:      sourceLocale,
		TargetLocale:      targetLocale,
		TargetRegister:    targetRegister,
		RegisterPolicy:    registerPolicy,
		MIMEType:          "text/plain",
		ProtectedTerms:    translation.NormalizeProtectedTerms(protectedTerms),
		StyleInstructions: nil,
	}
	profile.StyleInstructions = defaultTranslationStyleInstructions(profile)
	return profile
}

func inferTranslationContentKind(entityType string) translation.ContentKind {
	switch strings.TrimSpace(entityType) {
	case "email_template", "email_layout", "form", "campaign":
		return translation.ContentKindDirectUserGuidance
	case "privacy", "terms":
		return translation.ContentKindLegal
	case "menu":
		return translation.ContentKindNavigation
	default:
		return translation.ContentKindEditorial
	}
}

func inferTranslationRegisterPolicy(sourceLocale string) translation.RegisterPolicy {
	switch normalizeTranslationLocaleLanguage(sourceLocale) {
	case localization.LocaleKorean, localization.LocaleJapanese:
		return translation.RegisterPolicyPreserveSourceWhenClear
	default:
		return translation.RegisterPolicyTargetDefault
	}
}

func inferTargetTranslationRegister(
	contentKind translation.ContentKind,
	targetLocale string,
) translation.Register {
	switch contentKind {
	case translation.ContentKindDirectUserGuidance:
		return translation.RegisterPolite
	case translation.ContentKindLegal:
		return translation.RegisterFormalDocument
	default:
		switch normalizeTranslationLocaleLanguage(targetLocale) {
		case localization.LocaleKorean, localization.LocaleJapanese:
			return translation.RegisterNeutralPlain
		default:
			return translation.RegisterNeutralPlain
		}
	}
}

func defaultTranslationStyleInstructions(profile translation.GenerationProfile) []string {
	instructions := make([]string, 0, 4)
	if profile.RegisterPolicy == translation.RegisterPolicyPreserveSourceWhenClear {
		instructions = append(instructions, "If the source language has a clear honorific or plain register, preserve that register instead of forcing a different target register.")
	}

	targetLanguage := normalizeTranslationLocaleLanguage(profile.TargetLocale)
	switch profile.TargetRegister {
	case translation.RegisterPolite:
		switch targetLanguage {
		case localization.LocaleKorean:
			instructions = append(instructions, "Use consistent polite Korean appropriate for direct user guidance, using natural -습니다 or -요 endings.")
		case localization.LocaleJapanese:
			instructions = append(instructions, "Use consistent polite Japanese appropriate for direct user guidance, using natural desu/masu style.")
		default:
			instructions = append(instructions, "Use a polite, direct style appropriate for user-facing guidance.")
		}
	case translation.RegisterFormalDocument:
		switch targetLanguage {
		case localization.LocaleKorean:
			instructions = append(instructions, "Use formal documentary Korean appropriate for policy or legal text, avoiding conversational polite address.")
		case localization.LocaleJapanese:
			instructions = append(instructions, "Use formal documentary Japanese appropriate for policy or legal text, avoiding conversational polite address.")
		default:
			instructions = append(instructions, "Use formal documentary style appropriate for policy or legal text.")
		}
	default:
		switch targetLanguage {
		case localization.LocaleKorean:
			instructions = append(instructions, "Use neutral written Korean plain style ending in -다 or -한다; do not mix in polite -습니다 or -요 endings unless the content is direct user guidance.")
		case localization.LocaleJapanese:
			instructions = append(instructions, "Use neutral written Japanese plain style, da/de-aru style, and do not mix in desu/masu endings unless the content is direct user guidance.")
		default:
			instructions = append(instructions, "Use neutral written style unless the content kind requires direct user guidance or legal documentary style.")
		}
	}

	if len(profile.ProtectedTerms) > 0 {
		instructions = append(instructions, "Preserve protected names, titles, handles, URLs, catalog numbers, and canonical entity labels exactly when they appear in the source.")
	}
	return instructions
}

func normalizeTranslationLocaleLanguage(locale string) string {
	// Style profiles are language-wide by design. This intentionally reduces a
	// validated locale such as pt-BR or zh-TW to its base language and is not a
	// persistence or request-locale normalizer.
	trimmed := strings.TrimSpace(strings.ToLower(locale))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	if before, _, ok := strings.Cut(trimmed, "-"); ok {
		return before
	}
	return trimmed
}
