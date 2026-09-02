package form

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestToProtoFormSummaryUsesSourceTitleOverride(t *testing.T) {
	form := &model.Form{
		ID:        "form-1",
		Status:    model.FormStatus("FORM_STATUS_DRAFT"),
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}

	summary := toProtoFormSummaryWithSubmissionCount(form, "문의 폼", 3)
	assert.Equal(t, "문의 폼", summary.Title)
	assert.Equal(t, int32(3), summary.SubmissionCount)
}
