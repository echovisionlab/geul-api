package form

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalizeLocalizedFormSchema(t *testing.T) {
	t.Parallel()

	sourceSchema := []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email"}]},{"id":"step-2","title":"Phone","fields":[{"id":"field-phone","key":"phone","label":"Phone number","type":"tel","defaultCountry":"US"}]}]}`)
	localizedSchema := []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]},{"id":"step-deleted","title":"삭제된 단계","fields":[{"id":"field-legacy","key":"legacy","label":"예전 필드","type":"text"}]}]}`)

	canonicalSchema, canonicalText, err := CanonicalizeLocalizedFormSchema(sourceSchema, localizedSchema)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email"}]},{"id":"step-2","title":"Phone","fields":[{"id":"field-phone","key":"phone","label":"Phone number","type":"tel","defaultCountry":"US"}]}]}`,
		string(canonicalSchema),
	)
	require.NotNil(t, canonicalText)
	assert.Equal(t, "문의\n이메일\nPhone\nPhone number", *canonicalText)
}

func TestCanonicalizeLocalizedFormSchemaRemovesTargetOwnedStructure(t *testing.T) {
	t.Parallel()

	sourceSchema := []byte(`{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email","required":true}]}]}`)
	localizedSchema := []byte(`{"id":"target-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"changed-key","label":"이메일","type":"text","required":false}]},{"id":"target-only","title":"추가 단계","fields":[]}]}`)

	canonicalSchema, _, err := CanonicalizeLocalizedFormSchema(sourceSchema, localizedSchema)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email","required":true}]}]}`, string(canonicalSchema))
	assert.NotEqual(t, string(localizedSchema), string(canonicalSchema))
}

func TestCanonicalizeLocalizedFormSchemaMatchesOptionLabelsByValueWhenIDsAreAbsent(t *testing.T) {
	t.Parallel()

	sourceSchema := []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-topic","key":"topic","label":"Topic","type":"select","options":[{"value":"billing","label":"Billing"},{"value":"support","label":"Support"}]}]}]}`)
	localizedSchema := []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-topic","key":"topic","label":"주제","type":"select","options":[{"value":"support","label":"지원"},{"value":"billing","label":"청구"}]}]}]}`)

	canonicalSchema, canonicalText, err := CanonicalizeLocalizedFormSchema(sourceSchema, localizedSchema)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"id":"schema-1","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-topic","key":"topic","label":"주제","type":"select","options":[{"value":"billing","label":"청구"},{"value":"support","label":"지원"}]}]}]}`,
		string(canonicalSchema),
	)
	require.NotNil(t, canonicalText)
	assert.Equal(t, "문의\n주제\n청구\n지원", *canonicalText)
}

func TestNormalizeLocalizedFormSchemaOverlayKeepsMissingAndExplicitEmptyDistinct(t *testing.T) {
	t.Parallel()

	source := []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Current title","description":"Current description","fields":[]}]}`)
	target := []byte(`{"id":"ignored","steps":[{"id":"step-1","title":"","fields":[]},{"id":"target-only","title":"remove","fields":[]}]}`)

	overlay, _, err := NormalizeLocalizedFormSchemaOverlay(source, target)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"schema-1","steps":[{"id":"step-1","title":"","fields":[]}]}`, string(overlay))

	materialized, _, err := CanonicalizeLocalizedFormSchema(source, overlay)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"schema-1","steps":[{"id":"step-1","title":"","description":"Current description","fields":[]}]}`, string(materialized))
}
