package email

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateLayoutHTMLContentAcceptsSingleDocument(t *testing.T) {
	issues := ValidateLayoutHTMLContent(`<!DOCTYPE html><html><body><main>{{ content }}</main></body></html>`)

	assert.Empty(t, issues)
}

func TestValidateLayoutHTMLContentRejectsConcatenatedDocuments(t *testing.T) {
	issues := ValidateLayoutHTMLContent(
		`<!DOCTYPE html><html><body>{{content}}</body></html>` +
			`<!DOCTYPE html><html><body>{{content}}</body></html>`,
	)

	assert.Contains(t, layoutValidationIssueCodes(issues), "MULTIPLE_CONTENT_PLACEHOLDERS")
	assert.Contains(t, layoutValidationIssueCodes(issues), "MULTIPLE_HTML_DOCUMENTS")
}

func TestValidateLayoutHTMLContentRejectsMissingPlaceholder(t *testing.T) {
	issues := ValidateLayoutHTMLContent(`<!DOCTYPE html><html><body>No body slot</body></html>`)

	assert.Contains(t, layoutValidationIssueCodes(issues), "MISSING_CONTENT_PLACEHOLDER")
}

func TestValidateLayoutHTMLContentRejectsEmptyAndUnbalancedHTML(t *testing.T) {
	issues := ValidateLayoutHTMLContent(`  `)
	assert.Contains(t, layoutValidationIssueCodes(issues), "EMPTY_CONTENT")

	issues = ValidateLayoutHTMLContent(`<!DOCTYPE html><html><body><div>{{content}}</body></html>`)
	assert.Contains(t, layoutValidationIssueCodes(issues), "UNCLOSED_TAG")
}

func TestValidateLayoutHTMLContentAllowsVoidElements(t *testing.T) {
	issues := ValidateLayoutHTMLContent(`<html><body><img src="logo.png"><br><hr>{{content}}</body></html>`)

	assert.Empty(t, issues)
}

func TestValidateLayoutHTMLContentErrorReturnsFirstIssue(t *testing.T) {
	assert.NoError(t, ValidateLayoutHTMLContentError(`<main>{{content}}</main>`))

	err := ValidateLayoutHTMLContentError(`<main>No content placeholder</main>`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "MISSING_CONTENT_PLACEHOLDER")
}

func layoutValidationIssueCodes(issues []LayoutValidationIssue) []string {
	codes := make([]string, len(issues))
	for i, issue := range issues {
		codes[i] = issue.Code
	}
	return codes
}
