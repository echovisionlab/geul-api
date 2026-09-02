package public

import (
	"testing"
	"time"

	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProtoFormMetadataUsesSourceTitle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	formID := "form-metadata-source"
	form := &model.Form{
		ID:        formID,
		IsPublic:  true,
		CreatedAt: now,
	}
	sourceDocument := &formdomain.FormSourceDocument{
		Title:  "문의 폼",
		Schema: []byte(`{"id":"schema-1","steps":[]}`),
	}

	metadata := buildProtoFormMetadataFromResolvedDocument(
		form,
		sourceDocument,
		nil,
		openv1.FormStatus_FORM_STATUS_PUBLISHED,
		nil,
		nil,
	)

	assert.Equal(t, "문의 폼", metadata.Title)
	assert.Nil(t, metadata.OgAsset)
}

func TestBuildProtoFormMetadataIncludesOgFallbackAssets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	form := &model.Form{
		ID:        "form-metadata-assets",
		IsPublic:  true,
		CreatedAt: now,
	}
	sourceDocument := &formdomain.FormSourceDocument{Title: "Contact form"}
	ogAsset := &commonv1.AssetRef{AssetId: "generated-og", Url: "https://cdn.example.com/generated-og.webp"}
	featuredImageAsset := &commonv1.AssetRef{AssetId: "featured-image", Url: "https://cdn.example.com/featured.webp"}

	metadata := buildProtoFormMetadataFromResolvedDocument(
		form,
		sourceDocument,
		nil,
		openv1.FormStatus_FORM_STATUS_PUBLISHED,
		ogAsset,
		featuredImageAsset,
	)

	assert.Equal(t, ogAsset, metadata.GetOgAsset())
	assert.Equal(t, featuredImageAsset, metadata.GetFeaturedImageAsset())
}

func TestBuildProtoFormUsesLocalizedFormTranslationWhenRequested(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	formID := "form-metadata-localized"
	form := &model.Form{
		ID:        formID,
		IsPublic:  true,
		CreatedAt: now,
	}
	sourceDocument := &formdomain.FormSourceDocument{
		Title:       "Contact form",
		Schema:      []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]}]}`),
		ContentText: new("Contact\nEmail"),
	}
	localization := localizedContentSelection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		Title:           new("문의 폼"),
		ContentJSON:     []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`),
		ContentText:     new("문의\n이메일"),
	}
	applyLocalizedFormDocument(sourceDocument, localization)

	protoForm := buildProtoFormFromResolvedDocument(
		form,
		sourceDocument,
		&localization,
		openv1.FormStatus_FORM_STATUS_PUBLISHED,
		nil,
		nil,
	)

	assert.Equal(t, "문의 폼", protoForm.Title)
	assert.Equal(t, "ko", protoForm.GetLocalizationInfo().GetDisplayedLocale())
	assert.JSONEq(t, `{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`, string(protoForm.Schema))
	assert.Nil(t, protoForm.OgAsset)
}

func TestApplyLocalizedFormDocumentCanonicalizesAgainstCurrentSourceStructure(t *testing.T) {
	sourceDocument := &formdomain.FormSourceDocument{
		Title:       "Contact form",
		Schema:      []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]}]}`),
		ContentText: new("Contact\nEmail"),
	}
	localization := localizedContentSelection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		Title:           new("문의 폼"),
		ContentJSON:     []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]},{"id":"step-2","title":"오래된 단계","fields":[{"id":"field-phone","key":"phone","label":"전화번호","type":"tel"}]}]}`),
		ContentText:     new("문의\n이메일\n오래된 단계\n전화번호"),
	}

	applyLocalizedFormDocument(sourceDocument, localization)

	assert.Equal(t, "문의 폼", sourceDocument.Title)
	assert.JSONEq(
		t,
		`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`,
		string(sourceDocument.Schema),
	)
	require.NotNil(t, sourceDocument.ContentText)
	assert.Equal(t, "문의\n이메일", *sourceDocument.ContentText)
}
