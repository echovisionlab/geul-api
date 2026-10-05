package kratoswebhook

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/echovisionlab/geul-api/internal/structured"
)

type kratosHookErrorResponse struct {
	Messages []kratosHookMessageGroup `json:"messages"`
}

type kratosHookMessage struct {
	ID      int               `json:"id"`
	Text    string            `json:"text"`
	Type    string            `json:"type"`
	Context map[string]string `json:"context,omitempty"`
}

type kratosHookMessageGroup struct {
	InstancePtr string              `json:"instance_ptr"`
	Messages    []kratosHookMessage `json:"messages"`
}

func Decode(
	w http.ResponseWriter,
	r *http.Request,
	destination structured.Value, logMessage string,
) bool {
	if err := json.NewDecoder(r.Body).Decode(destination); err != nil {
		slog.ErrorContext(r.Context(), logMessage, "error", err, "error_code", "hook_payload_invalid")
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func RequirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodPost {
		return true
	}
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	return false
}

func WriteEmpty(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(structured.Fields{}); err != nil {
		slog.Error("Failed to encode hook response", "error", err)
	}
}

func WriteError(w http.ResponseWriter, status int, message string, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(kratosHookErrorResponse{
		Messages: []kratosHookMessageGroup{
			{
				InstancePtr: "#/method",
				Messages: []kratosHookMessage{
					{
						ID:   4900001,
						Text: message,
						Type: "error",
						Context: map[string]string{
							"reason": code,
						},
					},
				},
			},
		},
	}); err != nil {
		slog.Error("Failed to encode Kratos hook error response", "error", err)
	}
}
