package form

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFormSubmissionAgainstSchema(t *testing.T) {
	t.Parallel()
	schema := []byte(`{
		"id":"contact",
		"steps":[{"id":"main","fields":[
			{"id":"kind","key":"kind","type":"select","options":[{"value":"person","label":"Person"},{"value":"company","label":"Company"}]},
			{"id":"email","key":"email","type":"email","validation":{"validators":[{"predicate":"required"}]}},
			{"id":"company","key":"company","type":"text","condition":{"fieldId":"kind","operator":"eq","value":"company"},"validation":{"validators":[{"predicate":"required"}]}}
		]}]
	}`)

	require.NoError(t, ValidateFormSubmissionAgainstSchema(schema, []byte(`{"kind":"person","email":"hello@example.com"}`)))
	require.NoError(t, ValidateFormSubmissionAgainstSchema(schema, []byte(`{"kind":"company","email":"hello@example.com","company":"Geul"}`)))

	cases := []struct {
		name string
		data string
		want string
	}{
		{name: "unknown key", data: `{"kind":"person","email":"hello@example.com","extra":true}`, want: "unknown field"},
		{name: "invalid option", data: `{"kind":"other","email":"hello@example.com"}`, want: "available option"},
		{name: "invalid type", data: `{"kind":"person","email":42}`, want: "must be a string"},
		{name: "missing required", data: `{"kind":"person"}`, want: "is required"},
		{name: "active conditional required", data: `{"kind":"company","email":"hello@example.com"}`, want: "is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFormSubmissionAgainstSchema(schema, []byte(tc.data))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}
