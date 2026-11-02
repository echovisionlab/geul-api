package public

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/form"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormMetadataMessageUsesSourceTitle(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	metadata := formMessage(View{
		Form:     form.Form{ID: "form-metadata-source", IsPublic: true, CreatedAt: now, Status: form.StatusPublished},
		Metadata: true,
		Document: form.FormSourceDocument{Title: "문의 폼", Schema: []byte(`{"id":"schema-1","steps":[]}`)},
	})

	assert.Equal(t, "문의 폼", metadata.Title)
	assert.Nil(t, metadata.Schema, "metadata leaves out the schema")
	assert.Equal(t, openv1.FormStatus_FORM_STATUS_PUBLISHED, metadata.Status)
	assert.Nil(t, metadata.OgAsset)
	assert.Nil(t, metadata.LocalizationInfo)
}

func TestFormMetadataMessageIncludesOgFallbackAssets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	metadata := formMessage(View{
		Form:          form.Form{ID: "form-metadata-assets", IsPublic: true, CreatedAt: now},
		Metadata:      true,
		Document:      form.FormSourceDocument{Title: "Contact form"},
		OgAsset:       &form.Asset{ID: "generated-og", URL: "https://cdn.example.com/generated-og.webp"},
		FeaturedImage: &form.Asset{ID: "featured-image", URL: "https://cdn.example.com/featured.webp"},
	})

	assert.Equal(t, &commonv1.AssetRef{AssetId: "generated-og", Url: "https://cdn.example.com/generated-og.webp"}, metadata.GetOgAsset())
	assert.Equal(t, &commonv1.AssetRef{AssetId: "featured-image", Url: "https://cdn.example.com/featured.webp"}, metadata.GetFeaturedImageAsset())
}

func TestFormMessageUsesLocalizedFormTranslationWhenRequested(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	sourceDocument := form.FormSourceDocument{
		Title:       "Contact form",
		Schema:      []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]}]}`),
		ContentText: new("Contact\nEmail"),
	}
	localization := LocalizationSelection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		Title:           new("문의 폼"),
		ContentJSON:     []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`),
		ContentText:     new("문의\n이메일"),
	}
	applyLocalizedFormDocument(t.Context(), &sourceDocument, localization)

	protoForm := formMessage(View{
		Form:     form.Form{ID: "form-metadata-localized", IsPublic: true, CreatedAt: now, Status: form.StatusPublished},
		Document: sourceDocument, Localization: &localization,
	})

	assert.Equal(t, "문의 폼", protoForm.Title)
	assert.Equal(t, "ko", protoForm.GetLocalizationInfo().GetDisplayedLocale())
	assert.JSONEq(t, `{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`, string(protoForm.Schema))
	assert.Nil(t, protoForm.OgAsset)
}

func TestApplyLocalizedFormDocumentCanonicalizesAgainstCurrentSourceStructure(t *testing.T) {
	sourceDocument := &form.FormSourceDocument{
		Title:       "Contact form",
		Schema:      []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]}]}`),
		ContentText: new("Contact\nEmail"),
	}
	localization := LocalizationSelection{
		DisplayedLocale: "ko",
		SourceLocale:    "en",
		Title:           new("문의 폼"),
		ContentJSON:     []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]},{"id":"step-2","title":"오래된 단계","fields":[{"id":"field-phone","key":"phone","label":"전화번호","type":"tel"}]}]}`),
		ContentText:     new("문의\n이메일\n오래된 단계\n전화번호"),
	}

	applyLocalizedFormDocument(t.Context(), sourceDocument, localization)

	assert.Equal(t, "문의 폼", sourceDocument.Title)
	assert.JSONEq(
		t,
		`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]}]}`,
		string(sourceDocument.Schema),
	)
	require.NotNil(t, sourceDocument.ContentText)
	assert.Equal(t, "문의\n이메일", *sourceDocument.ContentText)
}
