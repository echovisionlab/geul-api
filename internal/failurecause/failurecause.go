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
// command-specific values (recipient, codes, template data) to redact verbatim.
// Server-side context that an error in the chain keeps out of its Error text
// (see OperationContext) is rendered in place.
func Message(err error, private ...string) string {
	if err == nil {
		return ""
	}
	return redact(render(err), private)
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
	var walk func(error)
	walk = func(err error) {
		switch typed := err.(type) { //nolint:errorlint // the walk descends one node at a time and must stop at the outermost OperationContext
		case nil:
		case OperationContext:
			found = append(found, typed)
		case interface{ Unwrap() []error }:
			for _, wrapped := range typed.Unwrap() {
				walk(wrapped)
			}
		case interface{ Unwrap() error }:
			walk(typed.Unwrap())
		}
	}
	walk(err)
	return found
}

// Attr is the conventional slog attribute for a failure cause. The telemetry
// log normalizer reduces "error" attributes to their type, so the sanitized
// text travels under "failure_cause", which it keeps.
func Attr(err error, private ...string) slog.Attr {
	return slog.String("failure_cause", Message(err, private...))
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
