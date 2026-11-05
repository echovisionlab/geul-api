package application

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func TestApplyTranslationRuntimeSettingsUpdateChangesTheDefaultLocale(t *testing.T) {
	baseline := translationRuntimeSettings{DefaultLocale: "en"}
	updated, err := applyTranslationRuntimeSettingsUpdate(baseline, UpdateTranslationSettingsCommand{
		Settings:   &translationRuntimeSettings{DefaultLocale: " ko "},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"default_locale"}},
	})
	require.NoError(t, err)
	require.Equal(t, "ko", updated.DefaultLocale)
	require.Equal(t, []string{"default_locale"}, translationRuntimeSettingsChangedFields(baseline, updated))
	require.Empty(t, translationRuntimeSettingsChangedFields(updated, updated))
}

func TestApplyTranslationRuntimeSettingsUpdateRequiresMask(t *testing.T) {
	current := translationRuntimeSettings{DefaultLocale: "ko"}
	request := UpdateTranslationSettingsCommand{Settings: &translationRuntimeSettings{DefaultLocale: "ja"}}
	_, err := applyTranslationRuntimeSettingsUpdate(current, request)
	require.ErrorContains(t, err, "update_mask is required")

	request.UpdateMask = &fieldmaskpb.FieldMask{}
	_, err = applyTranslationRuntimeSettingsUpdate(current, request)
	require.ErrorContains(t, err, "update_mask must include at least one settings field")
}

func TestApplyTranslationRuntimeSettingsUpdateRejectsRemovedFields(t *testing.T) {
	_, err := applyTranslationRuntimeSettingsUpdate(translationRuntimeSettings{DefaultLocale: "en"}, UpdateTranslationSettingsCommand{
		Settings:   &translationRuntimeSettings{DefaultLocale: "en"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"protected_terms"}},
	})
	require.ErrorContains(t, err, `unsupported update_mask path "protected_terms"`)
}
