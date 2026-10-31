// Package failurecause turns an error into a loggable cause. It keeps the
// diagnostic text (wrapped causes, element names, SQL identifiers) and removes
// only values that must never reach logs: credentials, URL user info and query
// strings, email addresses, and caller-supplied private values.
package failurecause

import (
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
)

const (
	// Short values ("15", "ko") would erase unrelated parts of a message.
	minPrivateValueLength = 4
	maxMessageLength      = 2048
)

var (
	emailAddress = regexp.MustCompile(`[\pL\pN.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[\pL\pN-]+(?:\.[\pL\pN-]+)+`)
	urlUserInfo  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@]+@`)
	urlQuery     = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^\s?#"'<>]+)[?#][^\s"'<>]*`)
	authHeader   = regexp.MustCompile(`(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]+`)
	// Matches key=value, key: value and JSON "key":"value" forms, including
	// provider spellings such as SecretAccessKey and AccessKeyId.
	credential = regexp.MustCompile(`(?i)\b((?:password|passwd|pwd|secret|secret_?access_?key|access_?key_?id|api[_-]?key|token|access_?token|refresh_?token|client_?secret|cookie|authorization)"?\s*[=:]\s*)(?:(?:Bearer|Basic)\s+[^\s,;&]+|"[^"]*"|'[^']*'|[^\s,;&"]+)`)
)

// Message returns err's text with sensitive values removed. private lists
// command-specific values (recipient, codes, template data) to redact verbatim,
// in addition to the private values the chain carries (see PrivateValuer).
// Server-side context that an error in the chain keeps out of its Error text
// (see OperationContext) is rendered in place.
func Message(err error, private ...string) string {
	if err == nil {
		return ""
	}
	return redact(render(err), append(PrivateValues(err), private...))
}

// PrivateValuer is implemented by an error whose text can hold values that no
// log may keep, such as an upstream response body. Every sink that renders the
// error through Message redacts them, wherever the error is in the chain.
type PrivateValuer interface {
	error
	PrivateValues() []string
}

// PrivateValues returns the private values of every error in err's chain,
// including each branch of a joined error.
func PrivateValues(err error) []string {
	var values []string
	walk(err, func(node error) bool {
		if valuer, ok := node.(PrivateValuer); ok { //nolint:errorlint // walk visits every node itself
			values = append(values, valuer.PrivateValues()...)
		}
		return true
	})
	return values
}

// Attrs returns the identifier attributes added with WithAttrs anywhere in
// err's chain. An outer attribute wins over an inner one with the same key.
func Attrs(err error) []slog.Attr {
	var attrs []slog.Attr
	seen := map[string]bool{}
	walk(err, func(node error) bool {
		if annotated, ok := node.(*annotatedError); ok { //nolint:errorlint // walk visits every node itself
			for _, attr := range annotated.attrs {
				if !seen[attr.Key] {
					seen[attr.Key] = true
					attrs = append(attrs, attr)
				}
			}
		}
		return true
	})
	return attrs
}

// WithPrivate returns err carrying request values its text may contain, such
// as a nickname or e-mail address, so that whichever sink logs the failure
// redacts them. It returns nil for a nil err.
func WithPrivate(err error, values ...string) error {
	if err == nil || len(values) == 0 {
		return err
	}
	return &annotatedError{cause: err, private: values}
}

// WithAttrs returns err carrying bounded identifier attributes (entity type and
// ID, job ID, locale) for the one record that logs the failure. The request
// log adds them to request.completed. It returns nil for a nil err.
func WithAttrs(err error, attrs ...slog.Attr) error {
	if err == nil || len(attrs) == 0 {
		return err
	}
	return &annotatedError{cause: err, attrs: attrs}
}

// Annotate adds identifier attributes to the error *errp holds, if any. A
// handler whose every failure concerns the same object defers it once:
//
//	defer failurecause.Annotate(&err, slog.String("page_id", req.Msg.GetPageId()))
func Annotate(errp *error, attrs ...slog.Attr) {
	*errp = WithAttrs(*errp, attrs...)
}

// AnnotatePrivate adds private values, such as a requested file name, to the
// error *errp holds, if any. Defer it like Annotate.
func AnnotatePrivate(errp *error, values ...string) {
	*errp = WithPrivate(*errp, values...)
}

// annotatedError adds diagnosis to an error without changing its text or
// classification: errors.Is, errors.As and connect.CodeOf see the cause.
type annotatedError struct {
	cause   error
	private []string
	attrs   []slog.Attr
}

func (e *annotatedError) Error() string           { return e.cause.Error() }
func (e *annotatedError) Unwrap() error           { return e.cause }
func (e *annotatedError) PrivateValues() []string { return e.private }

// walk visits err and the errors it wraps, depth first through every branch
// of a joined error. visit returns false to skip what a node wraps.
func walk(err error, visit func(error) bool) {
	if err == nil || !visit(err) {
		return
	}
	switch typed := err.(type) { //nolint:errorlint // the walk descends one node at a time
	case interface{ Unwrap() []error }:
		for _, wrapped := range typed.Unwrap() {
			walk(wrapped, visit)
		}
	case interface{ Unwrap() error }:
		walk(typed.Unwrap(), visit)
	}
}

// OperationContext is implemented by an error whose Error text omits
// server-side context, such as a classified RPC error cause whose Error text is
// the client message. rendered is the text the error contributes to the
// enclosing chain's Error text; Message substitutes operation for it.
type OperationContext interface {
	error
	OperationContext() (rendered, operation string)
}

func render(err error) string {
	text := err.Error()
	for _, classified := range operationContexts(err) {
		if rendered, operation := classified.OperationContext(); rendered != "" {
			text = strings.Replace(text, rendered, operation, 1)
		}
	}
	return text
}

// operationContexts finds the outermost OperationContext errors in the chain.
// It does not descend into one: its operation already renders what it wraps.
func operationContexts(err error) []OperationContext {
	var found []OperationContext
	walk(err, func(node error) bool {
		classified, ok := node.(OperationContext) //nolint:errorlint // walk visits every node itself
		if ok {
			found = append(found, classified)
		}
		return !ok
	})
	return found
}

// Attr is the conventional slog attribute for a failure cause. The telemetry
// log normalizer reduces "error" attributes to their type, so the sanitized
// text travels under "failure_cause", which it keeps.
func Attr(err error, private ...string) slog.Attr {
	return slog.String("failure_cause", Message(err, private...))
}

// Args returns the failure_cause attribute followed by the identifier
// attributes err carries, as arguments for a sink that logs a failure itself
// (a queue consumer, a scheduler loop).
func Args(err error, private ...string) []any {
	failure := Of(err, private...)
	args := make([]any, 0, len(failure.Attrs)+1)
	args = append(args, slog.String("failure_cause", failure.Cause))
	for _, attr := range failure.Attrs {
		args = append(args, attr)
	}
	return args
}

// Panic returns the recovered value as a cause and a stack without rendered
// call arguments, which can contain request data.
func Panic(recovered any, private ...string) (cause string, stack string) {
	cause = redact(fmt.Sprint(recovered), private)
	lines := strings.Split(string(debug.Stack()), "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "\t") {
			if args := strings.LastIndexByte(line, '('); args > 0 {
				lines[i] = line[:args] + "(...)"
			}
		}
	}
	return cause, strings.Join(lines, "\n")
}

func redact(message string, private []string) string {
	values := append([]string(nil), private...)
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		if len([]rune(value)) >= minPrivateValueLength {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	message = urlUserInfo.ReplaceAllString(message, "${1}[redacted]@")
	message = urlQuery.ReplaceAllString(message, "${1}?[redacted]")
	message = credential.ReplaceAllString(message, "${1}[redacted]")
	message = authHeader.ReplaceAllString(message, "${1} [redacted]")
	message = emailAddress.ReplaceAllString(message, "[redacted-email]")
	if runes := []rune(message); len(runes) > maxMessageLength {
		message = string(runes[:maxMessageLength]) + "…[truncated]"
	}
	return message
}
