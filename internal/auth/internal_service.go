package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

func constantTimeStringEqual(expected, provided string) bool {
	expectedHash := sha256.Sum256([]byte(expected))
	providedHash := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) == 1
}

// NormalizeHeaderName validates and canonicalizes an HTTP header field name.
// Header names are part of the trust boundary, so callers must not silently
// fall back to a package constant when configuration is absent or malformed.
func NormalizeHeaderName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("header name is required")
	}
	if name != strings.TrimSpace(name) {
		return "", fmt.Errorf("header name must not contain leading or trailing whitespace")
	}
	for index := 0; index < len(name); index++ {
		if !isHeaderTokenByte(name[index]) {
			return "", fmt.Errorf("header name contains invalid character")
		}
	}
	return http.CanonicalHeaderKey(name), nil
}

// ValidateHeaderNames validates both configured trust-boundary headers and
// rejects names that would collide with browser credentials or HTTP framing.
func ValidateHeaderNames(authHeaderName, internalServiceHeaderName string) error {
	authHeader, err := NormalizeHeaderName(authHeaderName)
	if err != nil {
		return fmt.Errorf("AUTH_HEADER_NAME: %w", err)
	}
	internalServiceHeader, err := NormalizeHeaderName(internalServiceHeaderName)
	if err != nil {
		return fmt.Errorf("INTERNAL_SERVICE_HEADER_NAME: %w", err)
	}
	if strings.EqualFold(authHeader, internalServiceHeader) {
		return fmt.Errorf("AUTH_HEADER_NAME and INTERNAL_SERVICE_HEADER_NAME must be distinct")
	}
	if isReservedTrustBoundaryHeader(authHeader) {
		return fmt.Errorf("AUTH_HEADER_NAME must not be a reserved credential or framing header")
	}
	if isReservedTrustBoundaryHeader(internalServiceHeader) {
		return fmt.Errorf("INTERNAL_SERVICE_HEADER_NAME must not be a reserved credential or framing header")
	}
	return nil
}

func isHeaderTokenByte(value byte) bool {
	if value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' {
		return true
	}
	switch value {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

func isReservedTrustBoundaryHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "cookie", "x-session-id", "proxy-authorization", "proxy-authenticate",
		"host", "content-length", "connection", "transfer-encoding", "upgrade", "te", "trailer":
		return true
	default:
		return false
	}
}

// IsInternalServiceRequest reports whether the request carries the configured
// internal-service credential. It is also used by the outer telemetry ingress
// to decide whether an incoming request ID is trusted; route authorization
// still belongs to RequireInternalServiceSecret.
func IsInternalServiceRequest(secret, headerName string, request *http.Request) bool {
	if strings.TrimSpace(secret) == "" || request == nil || normalizedHeaderNameOrEmpty(headerName) == "" {
		return false
	}
	return constantTimeStringEqual(secret, request.Header.Get(headerName))
}

// RequireInternalServiceSecret protects an internal HTTP handler with an
// explicit shared secret. Network placement is defense in depth, not the
// authentication boundary.
func RequireInternalServiceSecret(secret, headerName string, next http.Handler) http.Handler {
	if strings.TrimSpace(secret) == "" {
		panic("internal service secret is required")
	}
	if normalizedHeaderNameOrEmpty(headerName) == "" {
		panic("internal service header name is required and must be a valid HTTP header name")
	}
	if next == nil {
		panic("internal service handler is required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !IsInternalServiceRequest(secret, headerName, r) {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func normalizedHeaderNameOrEmpty(name string) string {
	normalized, err := NormalizeHeaderName(name)
	if err != nil {
		return ""
	}
	return normalized
}
