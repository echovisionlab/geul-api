package emaildelivery

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const minRedactedValueLength = 4

var failureEmail = regexp.MustCompile(`(?i)[\pL\pN.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[\pL\pN.-]+`)
var failureURL = regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|smtp)://[^\s<>"']+`)
var failureCredential = regexp.MustCompile(`(?i)((?:"|')?(?:authorization|password|passwd|secret(?:_access_key|accesskey)?|access_?key_?id|token|credential|api[_-]?key)(?:"|')?\s*[=:]\s*)(?:"[^"]*"|'[^']*'|(?:Bearer|Basic)\s+[^\s,;]+|[^\s,;]+)`)

// FailureCause retains diagnostic messages (including wrapped causes), but
// strips command values, addresses, URLs, and credential assignments before
// logging. Raw template_data and provider requests are never logged.
func FailureCause(err error, job *managev1.SendEmailEvent) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if job != nil {
		values := []string{job.GetRecipient()}
		for _, value := range job.GetTemplateData() {
			values = append(values, value)
		}
		sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
		for _, value := range values {
			// Short values ("15", "ko") would erase unrelated parts of the message; codes,
			// addresses and URLs are long enough, and URLs/addresses are also matched below.
			if len([]rune(value)) >= minRedactedValueLength {
				message = strings.ReplaceAll(message, value, "[redacted]")
			}
		}
	}
	message = failureURL.ReplaceAllString(message, "[redacted-url]")
	message = failureEmail.ReplaceAllString(message, "[redacted-email]")
	return failureCredential.ReplaceAllString(message, "${1}[redacted]")
}

type deliveryRenderContextKey struct{}

// LogDeliveryFailure is shared by the application and worker boundary, before
// retry/terminal mapping can discard a cause. Layout context comes from the
// renderer and is never reconstructed from recipient data.
func LogDeliveryFailure(ctx context.Context, event string, job *managev1.SendEmailEvent, err error, extra ...any) {
	layoutID, locale := "", job.GetLocale()
	if rendered, ok := ctx.Value(deliveryRenderContextKey{}).(*email.RenderedEmail); ok {
		layoutID, locale = rendered.LayoutID, rendered.DisplayedLocale
	}
	var renderErr *email.RenderError
	if errors.As(err, &renderErr) {
		layoutID = renderErr.LayoutID
		locale = renderErr.Locale
	}
	attrs := []any{"error", FailureCause(err, job), "locale", locale, "layout_id", layoutID}
	attrs = append(attrs, extra...)
	logDeliveryLifecycle(ctx, slog.LevelError, event, job, "failed", "", "", attrs...)
}
