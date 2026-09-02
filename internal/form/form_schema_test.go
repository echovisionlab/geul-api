package form

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCanonicalFormSchemaAcceptsKeyBasedSchema(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "Contact",
				"fields": [
					{
						"id": "field-email",
						"key": "email",
						"label": "Email address",
						"type": "email"
					},
					{
						"id": "field-topic",
						"key": "topic",
						"label": "Topic",
						"type": "select",
						"options": [
							{ "id": "option-billing", "value": "billing", "label": "Billing" },
							{ "id": "option-support", "value": "support", "label": "Support" }
						]
					}
				]
			},
			{
				"id": "step-2",
				"title": "Follow up",
				"condition": {
					"fieldId": "field-topic",
					"operator": "eq",
					"value": "support"
				},
				"fields": [
					{
						"id": "field-details",
						"key": "details",
						"label": "Details",
						"type": "textarea",
						"condition": {
							"fieldId": "field-email",
							"operator": "exists"
						}
					}
				]
			}
		]
	}`)

	require.NoError(t, validateCanonicalFormSchema(schema))
}

func TestValidateCanonicalFormSchemaRequiresGeneratedStableIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  string
		message string
	}{
		{
			name:    "step missing",
			schema:  `{"id":"schema","steps":[{"id":"","fields":[]}]}`,
			message: "schema.steps[0].id is required",
		},
		{
			name:    "step all digits",
			schema:  `{"id":"schema","steps":[{"id":"123","fields":[]}]}`,
			message: `schema.steps[0].id "123" is invalid`,
		},
		{
			name:    "field all digits",
			schema:  `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"123","key":"name","type":"text"}]}]}`,
			message: `schema.steps[0].fields[0].id "123" is invalid`,
		},
		{
			name:    "option missing",
			schema:  `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","type":"select","options":[{"value":"a","label":"A"}]}]}]}`,
			message: "schema.steps[0].fields[0].options[0].id is required",
		},
		{
			name:    "option all digits",
			schema:  `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","type":"select","options":[{"id":"123","value":"a","label":"A"}]}]}]}`,
			message: `schema.steps[0].fields[0].options[0].id "123" is invalid`,
		},
		{
			name:    "validator missing",
			schema:  `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"name","type":"text","validation":{"validators":[{"predicate":"required"}]}}]}]}`,
			message: "schema.steps[0].fields[0].validation.validators[0].id is required",
		},
		{
			name:    "validator all digits",
			schema:  `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"name","type":"text","validation":{"validators":[{"id":"123","predicate":"required"}]}}]}]}`,
			message: `schema.steps[0].fields[0].validation.validators[0].id "123" is invalid`,
		},
		{
			name:    "validator invalid grammar",
			schema:  `{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"name","type":"text","validation":{"validators":[{"id":"invalid id","predicate":"required"}]}}]}]}`,
			message: `schema.steps[0].fields[0].validation.validators[0].id "invalid id" is invalid`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCanonicalFormSchema([]byte(test.schema))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestValidateCanonicalFormSchemaRequiresKindGlobalUniqueStableIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  string
		message string
	}{
		{
			name: "option IDs",
			schema: `{"id":"schema","steps":[{"id":"step-a","fields":[
				{"id":"field-a","key":"a","type":"select","options":[{"id":"option-shared","value":"a"}]},
				{"id":"field-b","key":"b","type":"select","options":[{"id":"option-shared","value":"b"}]}
			]}]}`,
			message: `options[0].id "option-shared" must be unique across the form`,
		},
		{
			name: "validator IDs",
			schema: `{"id":"schema","steps":[{"id":"step-a","fields":[
				{"id":"field-a","key":"a","type":"text","validation":{"validators":[{"id":"validator-shared","predicate":"required"}]}},
				{"id":"field-b","key":"b","type":"text","validation":{"validators":[{"id":"validator-shared","predicate":"required"}]}}
			]}]}`,
			message: `validators[0].id "validator-shared" must be unique across the form`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateCanonicalFormSchema([]byte(test.schema))
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestValidateCanonicalFormSchemaAllowsSameStableIDAcrossKinds(t *testing.T) {
	t.Parallel()

	schema := []byte(`{"id":"schema","steps":[{"id":"shared-id","fields":[{"id":"shared-id","key":"choice","type":"select","options":[{"id":"shared-id","value":"a"}],"validation":{"validators":[{"id":"shared-id","predicate":"required"}]}}]}]}`)
	require.NoError(t, validateCanonicalFormSchema(schema))
}

func TestFormSourceCreateAndUpdateRejectInvalidStableIdentityBeforePersistence(t *testing.T) {
	t.Parallel()

	invalid := []byte(`{"id":"schema","steps":[{"id":"step-a","fields":[{"id":"field-a","key":"choice","type":"select","options":[{"value":"a"}]}]}]}`)
	service := &FormService{}

	_, createErr := service.CreateForm(t.Context(), connect.NewRequest(&managev1.CreateFormRequest{
		Title: "Form", Schema: invalid,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(createErr))
	assert.Contains(t, createErr.Error(), "options[0].id is required")

	_, updateErr := service.prepareFormUpdate(
		t.Context(), nil, &model.Form{}, &managev1.UpdateFormRequest{Schema: invalid},
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(updateErr))
	assert.Contains(t, updateErr.Error(), "options[0].id is required")
}

func TestValidateCanonicalFormSchemaRejectsDuplicateFieldKeys(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "Contact",
				"fields": [
					{ "id": "field-email", "key": "email", "type": "email" },
					{ "id": "field-email-confirm", "key": "email", "type": "email" }
				]
			}
		]
	}`)

	err := validateCanonicalFormSchema(schema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `key "email" must be unique`)
}

func TestValidateCanonicalFormSchemaRejectsReservedSubmissionMetadataKeys(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "Contact",
				"fields": [
					{ "id": "field-locale", "key": "__meta.locale", "type": "text" }
				]
			}
		]
	}`)

	err := validateCanonicalFormSchema(schema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `key "__meta.locale" is reserved`)
}

func TestValidateCanonicalFormSchemaRejectsUnknownConditionFieldID(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "Contact",
				"fields": [
					{
						"id": "field-email",
						"key": "email",
						"type": "email",
						"condition": {
							"fieldId": "field-missing",
							"operator": "exists"
						}
					}
				]
			}
		]
	}`)

	err := validateCanonicalFormSchema(schema)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `fieldId "field-missing" does not exist in schema`)
}

func TestExtractFormFieldMetadataReturnsOrderedKeysAndAliases(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "Contact",
				"fields": [
					{
						"id": "field-name",
						"key": "full_name",
						"name": "legacy_name",
						"label": "Full name",
						"type": "text"
					},
					{
						"id": "field-email",
						"name": "email",
						"label": "Email address",
						"type": "email"
					}
				]
			}
		]
	}`)

	metadata := extractFormFieldMetadata(schema)
	keys, labels := metadata.OrderedFieldKeys, metadata.FieldLabels

	assert.Equal(t, []string{"full_name", "email"}, keys)
	assert.Equal(t, "Full name", labels["full_name"])
	assert.Equal(t, "Full name", labels["legacy_name"])
	assert.Equal(t, "Full name", labels["field-name"])
	assert.Equal(t, "Email address", labels["email"])
	assert.Equal(t, "Email address", labels["field-email"])
}

func TestNormalizeCollaborativeFormSchemaPatchAcceptsValidSchema(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "Contact",
				"fields": [
					{ "id": "field-email", "key": "email", "label": "Email", "type": "email" }
				]
			}
		]
	}`)

	normalized, err := normalizeCollaborativeFormSchemaPatch(schema)
	require.NoError(t, err)
	assert.Equal(t, schema, normalized)
}

func TestNormalizeCollaborativeFormSchemaPatchRejectsInvalidSchema(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"title": "",
				"showTitle": false,
				"fields": [
					{ "id": "field-email", "key": "email", "type": "email" }
				]
			}
		]
	}`)

	normalized, err := normalizeCollaborativeFormSchemaPatch(schema)
	require.NoError(t, err)
	assert.Equal(t, schema, normalized)
}

func TestNormalizeCollaborativeFormSchemaPatchRejectsMissingFieldKey(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"id": "schema-1",
		"steps": [
			{
				"id": "step-1",
				"fields": [
					{ "id": "field-email", "type": "email" }
				]
			}
		]
	}`)

	normalized, err := normalizeCollaborativeFormSchemaPatch(schema)
	require.Error(t, err)
	assert.Nil(t, normalized)
	assert.Contains(t, err.Error(), "schema.steps[0].fields[0].key is required")
}
