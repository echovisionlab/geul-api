package application

import (
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestTranslationJobSortKeepsUniqueTieBreaker(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	type sortCase struct {
		name  string
		sorts []*commonv1.SortSpec
		order string
	}
	tests := []sortCase{{name: "default", order: "updated_at DESC, id ASC"}}
	for _, field := range []string{"requested_at", "updated_at", "target_locale", "status"} {
		tests = append(tests,
			sortCase{field + " ascending", []*commonv1.SortSpec{{Field: field}}, field + " ASC,id ASC"},
			sortCase{field + " descending", []*commonv1.SortSpec{{Field: field, Order: commonv1.SortOrder_SORT_ORDER_DESC}}, field + " DESC,id ASC"},
		)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, err := applyTranslationJobSort(db.Model(&model.TranslationJob{}), test.sorts)
			if err != nil {
				t.Fatal(err)
			}
			var jobs []model.TranslationJob
			sql := query.Limit(2).Offset(2).Find(&jobs).Statement.SQL.String()
			if !strings.Contains(sql, "ORDER BY "+test.order+" LIMIT") {
				t.Fatalf("pagination ordering = %s, want %s", sql, test.order)
			}
		})
	}
	if _, err := applyTranslationJobSort(db.Model(&model.TranslationJob{}), []*commonv1.SortSpec{{Field: "unsupported"}}); err == nil {
		t.Fatal("unsupported translation Job sort was accepted")
	}
}

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
