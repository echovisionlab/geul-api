package member

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	emailutil "github.com/echovisionlab/geul-api/internal/email"
)

// EmailReuseReader reads the reuse hold of a normalized email.
type EmailReuseReader interface {
	EmailReuseHeld(context.Context, string) (bool, error)
}

// RegistrationEmailReuseBlocked checks the Member reuse hold after normalizing email.
func RegistrationEmailReuseBlocked(ctx context.Context, store EmailReuseReader, email string) (bool, error) {
	if store == nil {
		return false, fmt.Errorf("member database is required")
	}
	normalized, ok := NormalizeEmailInput(email)
	if !ok {
		return false, nil
	}
	return store.EmailReuseHeld(ctx, normalized)
}

// NormalizeEmailInput validates and canonicalizes a registration email.
func NormalizeEmailInput(email string) (string, bool) {
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
