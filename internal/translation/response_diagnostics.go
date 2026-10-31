package translation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/echovisionlab/geul-api/internal/failurecause"
)

// Response issue kinds are stable diagnostic codes for provider output that
// breaks the source contract. Logs and spans carry only these codes, counts,
// and source-owned group or unit IDs, never unit text.
const (
	responseIssueVersionChanged            = "version_changed"
	responseIssueSourceLocaleChanged       = "source_locale_changed"
	responseIssueTargetLocaleChanged       = "target_locale_changed"
	responseIssueFileIDChanged             = "file_id_changed"
	responseIssueGroupCountChanged         = "group_count_changed"
	responseIssueGroupChanged              = "group_changed"
	responseIssueGroupMetadataChanged      = "group_metadata_changed"
	responseIssueUnitOrderChanged          = "unit_order_changed"
	responseIssueSourceChanged             = "source_changed"
	responseIssueTargetEmpty               = "target_empty"
	responseIssueInlineStructureChanged    = "inline_structure_changed"
	responseIssueProtectedTermCountChanged = "protected_term_count_changed"
	responseIssueMissingUnit               = "missing_unit"
	responseIssueUnknownUnit               = "unknown_unit"
	responseIssueDuplicateUnit             = "duplicate_unit"
	responseIssueLineBreakChanged          = "line_break_changed"
	responseIssuePlaceholderChanged        = "placeholder_changed"
	responseIssueMissingPlaceholder        = "missing_placeholder"
	responseIssueUnexpectedPlaceholder     = "unexpected_placeholder"
	responseIssueEnvelopeInvalid           = "envelope_invalid"
	responseIssueDuplicateGroup            = "duplicate_group"
	responseIssueGroupMembershipChanged    = "group_membership_changed"
	responseIssueInlineInvalid             = "inline_invalid"
)

const (
	maxResponseIssueSamples     = 5
	maxResponseIssueIDRunes     = 96
	maxResponseIssueReasonRunes = 120
)

// ResponseIssue is one structural validation failure. ID is a group or unit ID
// from the expected source document. Reason is a fixed validator message.
type ResponseIssue struct {
	Kind   string
	ID     string
	Reason string
}

func (issue ResponseIssue) message() string {
	switch issue.Kind {
	case responseIssueVersionChanged:
		return "XLIFF version changed"
	case responseIssueSourceLocaleChanged:
		return "XLIFF source locale changed"
	case responseIssueTargetLocaleChanged:
		return "XLIFF target locale changed"
	case responseIssueFileIDChanged:
		return "XLIFF file id changed"
	case responseIssueGroupCountChanged:
		return "XLIFF group count changed"
	case responseIssueGroupChanged:
		return fmt.Sprintf("XLIFF group %q changed", issue.ID)
	case responseIssueGroupMetadataChanged:
		return fmt.Sprintf("XLIFF group %q metadata changed", issue.ID)
	case responseIssueUnitOrderChanged:
		return fmt.Sprintf("XLIFF group %q unit order changed", issue.ID)
	case responseIssueSourceChanged:
		return fmt.Sprintf("XLIFF unit %q source changed", issue.ID)
	case responseIssueTargetEmpty:
		return fmt.Sprintf("XLIFF unit %q target is empty", issue.ID)
	case responseIssueInlineStructureChanged:
		return fmt.Sprintf("XLIFF unit %q inline structure changed", issue.ID)
	case responseIssueProtectedTermCountChanged:
		return fmt.Sprintf("XLIFF unit %q protected term count changed", issue.ID)
	default:
		return "XLIFF response " + issue.Kind
	}
}

// responseIssueSummary accumulates bounded counts, sample IDs, and the first
// inline reason for one failure cause.
type responseIssueSummary struct {
	counts       map[string]int
	samples      []string
	extraSamples int
	inlineReason string
}

// add records one issue; id may be empty for document-level issues.
func (summary *responseIssueSummary) add(kind string, id string) {
	summary.addCount(kind, 1)
	summary.sample(kind, id)
}

func (summary *responseIssueSummary) addIDs(kind string, ids []string) {
	for _, id := range ids {
		summary.add(kind, id)
	}
}

func (summary *responseIssueSummary) addCount(kind string, count int) {
	if count == 0 {
		return
	}
	if summary.counts == nil {
		summary.counts = make(map[string]int)
	}
	summary.counts[kind] += count
}

func (summary *responseIssueSummary) sample(kind string, id string) {
	if id == "" {
		return
	}
	if len(summary.samples) >= maxResponseIssueSamples {
		summary.extraSamples++
		return
	}
	summary.samples = append(summary.samples, kind+"["+truncateRunes(id, maxResponseIssueIDRunes)+"]")
}

func (summary *responseIssueSummary) kinds() []string {
	kinds := make([]string, 0, len(summary.counts))
	for kind := range summary.counts {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

func (summary *responseIssueSummary) String() string {
	kinds := summary.kinds()
	counts := make([]string, len(kinds))
	for index, kind := range kinds {
		counts[index] = kind + ":" + strconv.Itoa(summary.counts[kind])
	}
	var builder strings.Builder
	builder.WriteString("issues=")
	builder.WriteString(strings.Join(counts, ","))
	if len(summary.samples) > 0 {
		builder.WriteString(" sample_ids=")
		builder.WriteString(strings.Join(summary.samples, ","))
		if summary.extraSamples > 0 {
			builder.WriteString(",+" + strconv.Itoa(summary.extraSamples))
		}
	}
	if summary.inlineReason != "" {
		builder.WriteString(" inline_reason=")
		builder.WriteString(summary.inlineReason)
	}
	return builder.String()
}

func summarizeHardValidation(validation HardValidationResult) *responseIssueSummary {
	summary := &responseIssueSummary{}
	for _, issue := range validation.Issues {
		summary.add(issue.Kind, issue.ID)
		if issue.Kind == responseIssueInlineStructureChanged && summary.inlineReason == "" {
			summary.inlineReason = truncateRunes(issue.Reason, maxResponseIssueReasonRunes)
		}
	}
	summary.addIDs(responseIssueMissingUnit, validation.MissingUnitIDs)
	summary.addIDs(responseIssueLineBreakChanged, validation.LineBreakMismatchUnitIDs)
	summary.addIDs(responseIssuePlaceholderChanged, validation.PlaceholderMismatchUnitIDs)
	// Unknown and duplicate IDs come from the provider, so only counts are kept.
	summary.addCount(responseIssueUnknownUnit, len(validation.UnknownUnitIDs))
	summary.addCount(responseIssueDuplicateUnit, len(validation.DuplicateUnitIDs))
	summary.addCount(responseIssueMissingPlaceholder, len(validation.MissingPlaceholders))
	summary.addCount(responseIssueUnexpectedPlaceholder, len(validation.UnexpectedPlaceholders))
	return summary
}

// ResponseValidationError converts a failed hard validation into the bounded
// invalid-response error. Its Error text stays generic; the issue summary is
// reserved for LogProviderResponseFailure and ProviderResponseIssueKinds.
func ResponseValidationError(validation HardValidationResult) error {
	summary := summarizeHardValidation(validation)
	return &translationResponseError{
		event:  "validation_failed",
		issues: summary.kinds(),
		cause:  errors.New("response violates XLIFF source contract: " + summary.String()),
	}
}

// ProviderResponseIssueKinds returns the bounded issue kinds attached to an
// invalid provider response error, or nil when err carries none.
func ProviderResponseIssueKinds(err error) []string {
	var responseErr *translationResponseError
	if !errors.As(err, &responseErr) || len(responseErr.issues) == 0 {
		return nil
	}
	return append([]string(nil), responseErr.issues...)
}

// LogProviderResponseFailure records why provider output was rejected, with
// the job context attached through WithProviderLogContext.
func LogProviderResponseFailure(ctx context.Context, req ProviderRequest, err error) {
	if err == nil {
		return
	}
	event := "translation.provider.response_invalid"
	cause := err
	var kinds []string
	var responseErr *translationResponseError
	if errors.As(err, &responseErr) {
		event = "translation.provider." + responseErr.event
		if responseErr.cause != nil {
			cause = responseErr.cause
		}
		kinds = responseErr.issues
	}
	fields := []any{
		"domain", "translation", "event", event, "outcome", "failed", failurecause.Attr(cause),
		"request_id", req.RequestID, "operation_id", req.OperationID,
	}
	if len(kinds) > 0 {
		fields = append(fields, "response_issues", strings.Join(kinds, ","))
	}
	if req.Profile.TargetLocale != "" {
		fields = append(fields, "target_locale", req.Profile.TargetLocale)
	}
	LogProviderEvent(ctx, slog.LevelError, "Translation provider response rejected", fields...)
}

type providerLogContextKey struct{}

// ProviderLogContext identifies the job a provider call serves. Provider
// boundary logs add these fields so a failure can be tied to its job even
// though failed jobs are deleted.
type ProviderLogContext struct {
	JobID        string
	EntityType   string
	EntityID     string
	TargetLocale string
	Provider     string
	Model        string
}

func WithProviderLogContext(ctx context.Context, logContext ProviderLogContext) context.Context {
	return context.WithValue(ctx, providerLogContextKey{}, logContext)
}

// LogProviderEvent logs a translation.provider.* record and appends the job
// context from ctx. Keys already present in args win over context fields.
func LogProviderEvent(ctx context.Context, level slog.Level, message string, args ...any) {
	if ctx == nil {
		ctx = context.Background()
	}
	logContext, ok := ctx.Value(providerLogContextKey{}).(ProviderLogContext)
	if ok {
		present := make(map[string]struct{}, len(args))
		for index := 0; index < len(args); index++ {
			switch arg := args[index].(type) {
			case slog.Attr:
				present[arg.Key] = struct{}{}
			case string:
				present[arg] = struct{}{}
				index++
			}
		}
		for _, field := range []struct{ key, value string }{
			{"job_id", logContext.JobID},
			{"entity_type", logContext.EntityType},
			{"entity_id", logContext.EntityID},
			{"target_locale", logContext.TargetLocale},
			{"provider", logContext.Provider},
			{"model", logContext.Model},
		} {
			if _, exists := present[field.key]; !exists && field.value != "" {
				args = append(args, field.key, field.value)
			}
		}
	}
	slog.Log(ctx, level, message, args...)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
