package release

import "strings"

// MergeCreditNotesForDisplay overlays the displayed locale's credit notes
// on the source-locale notes. Credit membership remains owned by
// release_credit: a note of a credit outside creditIDs is never surfaced,
// and a blank note shows nothing.
func MergeCreditNotesForDisplay(
	source map[string]string,
	localized map[string]string,
	creditIDs []string,
) map[string]string {
	allowed := make(map[string]struct{}, len(creditIDs))
	for _, creditID := range creditIDs {
		allowed[creditID] = struct{}{}
	}
	merged := make(map[string]string, len(source)+len(localized))
	for creditID, note := range source {
		if _, ok := allowed[creditID]; ok && strings.TrimSpace(note) != "" {
			merged[creditID] = strings.TrimSpace(note)
		}
	}
	for creditID, note := range localized {
		if _, ok := allowed[creditID]; ok && strings.TrimSpace(note) != "" {
			merged[creditID] = strings.TrimSpace(note)
		}
	}
	return merged
}
