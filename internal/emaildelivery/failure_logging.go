package emaildelivery

import (
	"context"
	"errors"
	"log/slog"
	"net/mail"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// FailureCause redacts private command values with the shared failurecause sanitizer.
func FailureCause(err error, job *managev1.SendEmailEvent) string {
	values := []string{job.GetRecipient()}
	if recipient, parseErr := mail.ParseAddress(job.GetRecipient()); parseErr == nil {
		values = append(values, recipient.Name)
	}
	for _, value := range job.GetTemplateData() {
		values = append(values, value)
	}
	return failurecause.Message(err, values...)
}

// requestPrivateValues are the recipient and the string template values of an
// incoming SendEmail request, which no failure log may keep.
func requestPrivateValues(request *intrav1.SendEmailRequest) []string {
	values := []string{request.GetRecipient()}
	if recipient, parseErr := mail.ParseAddress(request.GetRecipient()); parseErr == nil {
		values = append(values, recipient.Name)
	}
	for _, value := range request.GetTemplateData().AsMap() {
		if text, ok := value.(string); ok {
			values = append(values, text)
		}
	}
	return values
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
	attrs := []any{"failure_cause", FailureCause(err, job), "locale", locale, "layout_id", layoutID}
	attrs = append(attrs, extra...)
	logDeliveryLifecycle(ctx, slog.LevelError, event, job, "failed", "", "", attrs...)
}
