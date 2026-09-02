package account

import (
	"net/mail"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

func normalizeAccountEmailInput(email string) (string, bool) {
	normalized := emailutil.NormalizeAddressForDelivery(email)
	if normalized == "" || strings.HasSuffix(normalized, ".local") {
		return normalized, false
	}
	parsed, err := mail.ParseAddress(normalized)
	if err != nil {
		return normalized, false
	}
	return emailutil.NormalizeAddressForDelivery(parsed.Address), true
}

// NormalizeAccountEmailInput validates and canonicalizes an account email.
func NormalizeAccountEmailInput(email string) (string, bool) {
	return normalizeAccountEmailInput(email)
}
