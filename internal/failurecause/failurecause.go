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
func Message(err error, private ...string) string {
	if err == nil {
		return ""
	}
	return redact(err.Error(), private)
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
