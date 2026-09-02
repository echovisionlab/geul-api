package authentication

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

func stripUpstreamCORSHeaders(response *http.Response) error {
	for name := range response.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-") {
			response.Header.Del(name)
		}
	}

	seen := make(map[string]struct{})
	vary := make([]string, 0)
	for _, value := range response.Header.Values("Vary") {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" || strings.EqualFold(token, "Origin") {
				continue
			}
			key := strings.ToLower(token)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			vary = append(vary, token)
		}
	}
	response.Header.Del("Vary")
	if len(vary) > 0 {
		response.Header.Set("Vary", strings.Join(vary, ", "))
	}
	return nil
}

func kratosResponseAcceptedIssuance(statusCode int, body []byte) bool {
	if statusCode >= http.StatusOK && statusCode < http.StatusBadRequest {
		return true
	}
	var payload structured.Value
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return jsonTreeContainsString(payload, "state", "sent_email")
}

func jsonTreeContainsString(value structured.Value, key string, expected string) bool {
	switch typed := value.(type) {
	case structured.Fields:
		for childKey, child := range typed {
			if childKey == key {
				if text, ok := child.(string); ok && text == expected {
					return true
				}
			}
			if jsonTreeContainsString(child, key, expected) {
				return true
			}
		}
	case structured.Values:
		for _, child := range typed {
			if jsonTreeContainsString(child, key, expected) {
				return true
			}
		}
	}
	return false
}

func writeKratosProxyError(w http.ResponseWriter, status int, message string, retryAfter time.Duration) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		seconds := int64((retryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", fmt.Sprintf("%d", seconds))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(structured.Fields{
		"error": structured.Fields{
			"code":    status,
			"message": message,
		},
	})
}
