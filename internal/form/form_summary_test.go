package form

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFormSummaryMessageUsesSourceTitleOverride(t *testing.T) {
	summary := formSummaryMessage(Summary{
		Form: Form{
			ID:           "form-1",
			Status:       StatusDraft,
			CreatedAt:    time.Unix(1_700_000_000, 0).UTC(),
			SourceLocale: "ko",
		},
		Title: "문의 폼", SubmissionCount: 3,
	})
	assert.Equal(t, "문의 폼", summary.Title)
	assert.Equal(t, "ko", summary.SourceLocale)
	assert.Equal(t, int32(3), summary.SubmissionCount)
}
