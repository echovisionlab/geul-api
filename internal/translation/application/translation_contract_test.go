package application

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/translation"
)

func TestValidateGenerationProfile(t *testing.T) {
	t.Parallel()

	err := translation.ValidateGenerationProfile(translation.GenerationProfile{
		QualityTier:    translation.QualityTierHigh,
		PreserveMarkup: true,
		ContentKind:    translation.ContentKindEditorial,
		SourceLocale:   "en",
		TargetLocale:   "ko",
		TargetRegister: translation.RegisterNeutralPlain,
		RegisterPolicy: translation.RegisterPolicyTargetDefault,
		MIMEType:       "text/html",
	})
	if err != nil {
		t.Fatalf("translation.ValidateGenerationProfile() error = %v", err)
	}
}

func TestValidateGenerationProfileRejectsSameLocale(t *testing.T) {
	t.Parallel()

	err := translation.ValidateGenerationProfile(translation.GenerationProfile{
		QualityTier:  translation.QualityTierStandard,
		SourceLocale: "en",
		TargetLocale: "en",
		MIMEType:     "text/html",
	})
	if err == nil {
		t.Fatal("expected error when source and target locale match")
	}
}

func TestValidateGenerationProfileRejectsUnsupportedRegisterPolicy(t *testing.T) {
	t.Parallel()

	err := translation.ValidateGenerationProfile(translation.GenerationProfile{
		QualityTier:    translation.QualityTierStandard,
		ContentKind:    translation.ContentKindEditorial,
		SourceLocale:   "en",
		TargetLocale:   "ko",
		TargetRegister: translation.RegisterNeutralPlain,
		RegisterPolicy: "mixed",
		MIMEType:       "text/html",
	})
	if err == nil {
		t.Fatal("expected error for unsupported register policy")
	}
}

func TestValidateProviderRequestRequiresBundles(t *testing.T) {
	t.Parallel()

	err := translation.ValidateProviderRequest(translation.ProviderRequest{
		RequestID:   "req-1",
		OperationID: "op-1",
		Profile: translation.GenerationProfile{
			QualityTier:  translation.QualityTierStandard,
			SourceLocale: "en",
			TargetLocale: "ja",
			MIMEType:     "text/html",
		},
	})
	if err == nil {
		t.Fatal("expected bundle validation error")
	}
}
