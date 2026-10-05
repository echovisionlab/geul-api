package application

import (
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestTranslationRuntimeSettingsNormalizeProtectedTermsBeforeCompareAndProjection(t *testing.T) {
	settings, err := translationRuntimeSettingsFromProto(&managev1.TranslationSettings{
		DefaultLocale: "en", ProtectedTerms: []string{" Photoshop ", "Photoshop", "react native", "React Native", " "},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"Photoshop", "react native", "React Native"}, settings.ProtectedTerms)
	require.Equal(t, settings.ProtectedTerms, toProtoTranslationSettings(settings).ProtectedTerms)
	require.Empty(t, translationRuntimeSettingsChangedFields(settings, translationRuntimeSettings{
		DefaultLocale: "en", ProtectedTerms: []string{"Photoshop", "react native", "React Native"},
	}))
	require.Equal(t, []string{"protected_terms"}, translationRuntimeSettingsChangedFields(settings, translationRuntimeSettings{
		DefaultLocale: "en", ProtectedTerms: []string{"Photoshop", "React Native"},
	}))
}

func TestMergeProtectedTermsDeltaPreservesConcurrentDisjointEdits(t *testing.T) {
	current := []string{"React Native", "Remote addition", "Remove me"}
	observed := []string{"React Native", "Remove me"}
	requested := []string{"React Native", "Local addition"}

	require.Equal(t,
		[]string{"React Native", "Remote addition", "Local addition"},
		mergeProtectedTermsDelta(current, observed, requested),
	)
}

func TestMergeProtectedTermsDeltaCanExplicitlyClearObservedTerms(t *testing.T) {
	require.Equal(t,
		[]string{"Concurrent addition"},
		mergeProtectedTermsDelta([]string{"Observed term", "Concurrent addition"}, []string{"Observed term"}, nil),
	)
}

func TestApplyTranslationRuntimeSettingsUpdatePreservesUntouchedFields(t *testing.T) {
	baseline := translationRuntimeSettings{DefaultLocale: "en", ProtectedTerms: []string{"Observed term"}}
	localePatch := &managev1.UpdateTranslationSettingsRequest{
		Settings:   &managev1.TranslationSettings{DefaultLocale: "ko"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"default_locale"}},
	}
	updatedLocale, err := applyTranslationRuntimeSettingsUpdate(baseline, localePatch)
	require.NoError(t, err)
	require.Equal(t, "ko", updatedLocale.DefaultLocale)
	require.Equal(t, []string{"Observed term"}, updatedLocale.ProtectedTerms)

	staleTermsPatch := &managev1.UpdateTranslationSettingsRequest{
		Settings:   &managev1.TranslationSettings{ProtectedTerms: []string{"Observed term", "Local addition"}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"protected_terms"}},
		BaseSettings: &managev1.TranslationSettings{
			DefaultLocale: "en", ProtectedTerms: []string{"Observed term"},
		},
	}
	updatedTerms, err := applyTranslationRuntimeSettingsUpdate(updatedLocale, staleTermsPatch)
	require.NoError(t, err)
	require.Equal(t, "ko", updatedTerms.DefaultLocale)
	require.Equal(t, []string{"Observed term", "Local addition"}, updatedTerms.ProtectedTerms)
}

func TestApplyTranslationRuntimeSettingsUpdateMergesConcurrentProtectedTermChanges(t *testing.T) {
	current := translationRuntimeSettings{
		DefaultLocale:  "en",
		ProtectedTerms: []string{"Observed term", "Remote addition", "Remove me"},
	}
	request := &managev1.UpdateTranslationSettingsRequest{
		Settings:   &managev1.TranslationSettings{ProtectedTerms: []string{"Observed term", "Local addition"}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"protected_terms"}},
		BaseSettings: &managev1.TranslationSettings{
			DefaultLocale: "en", ProtectedTerms: []string{"Observed term", "Remove me"},
		},
	}

	updated, err := applyTranslationRuntimeSettingsUpdate(current, request)
	require.NoError(t, err)
	require.Equal(t, []string{"Observed term", "Remote addition", "Local addition"}, updated.ProtectedTerms)
}

func TestApplyTranslationRuntimeSettingsUpdateRequiresMask(t *testing.T) {
	current := translationRuntimeSettings{DefaultLocale: "ko", ProtectedTerms: []string{"Existing term"}}
	request := &managev1.UpdateTranslationSettingsRequest{
		Settings: &managev1.TranslationSettings{DefaultLocale: "ja", ProtectedTerms: []string{"Replacement term"}},
	}
	_, err := applyTranslationRuntimeSettingsUpdate(current, request)
	require.ErrorContains(t, err, "update_mask is required")

	request.UpdateMask = &fieldmaskpb.FieldMask{}
	_, err = applyTranslationRuntimeSettingsUpdate(current, request)
	require.ErrorContains(t, err, "update_mask must include at least one settings field")
}

func TestApplyTranslationRuntimeSettingsUpdateRequiresObservedBaseForProtectedTerms(t *testing.T) {
	current := translationRuntimeSettings{DefaultLocale: "en", ProtectedTerms: []string{"Existing term"}}
	request := &managev1.UpdateTranslationSettingsRequest{
		Settings:   &managev1.TranslationSettings{ProtectedTerms: []string{"Replacement term"}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"protected_terms"}},
	}
	_, err := applyTranslationRuntimeSettingsUpdate(current, request)
	require.ErrorContains(t, err, "base_settings are required when patching protected_terms")
}
