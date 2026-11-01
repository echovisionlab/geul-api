package series

// parseSeriesStatus reads a Series status a client names by its API enum
// name. It reports false for UNSPECIFIED and unknown names.
func parseSeriesStatus(name string) (Status, bool) {
	switch status := Status(name); status {
	case StatusDraft, StatusPublished:
		return status, true
	default:
		return "", false
	}
}
