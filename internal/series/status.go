package series

import (
	"github.com/echovisionlab/geul-api/internal/model"
)

// parseSeriesStatus reads a Series status a client names by its API enum
// name. It reports false for UNSPECIFIED and unknown names.
func parseSeriesStatus(name string) (model.SeriesStatus, bool) {
	switch status := model.SeriesStatus(name); status {
	case model.SeriesStatusDraft, model.SeriesStatusPublished:
		return status, true
	default:
		return "", false
	}
}
