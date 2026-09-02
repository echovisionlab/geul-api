package email

import "strings"

// NormalizeAddressForDelivery returns the canonical comparison form used at
// authentication and delivery boundaries. It does not establish email proof.
func NormalizeAddressForDelivery(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
