package aidocument

import (
	"log/slog"
	"strconv"
	"strings"
)

const (
	maxLoggedIssues      = 5
	maxLoggedHandleRunes = 64
)

// IssuesLogAttr is the shared "issues" log attribute for rejected AI document
// mutations. Each entry is "op<index>:<code>@<handle>", up to five entries,
// followed by "+N more" when issues were omitted. Messages are left out
// because they can quote document text; pair this with "issue_count".
//
// The value is one string because the telemetry normalizer drops slice and
// struct values (slog.KindAny) before JSON or OTLP encoding.
func IssuesLogAttr(issues []OperationIssue) slog.Attr {
	shown := issues
	if len(shown) > maxLoggedIssues {
		shown = shown[:maxLoggedIssues]
	}
	entries := make([]string, 0, maxLoggedIssues+1)
	for _, issue := range shown {
		entry := "op" + strconv.Itoa(issue.Operation) + ":" + string(issue.Code)
		if handle := boundedHandle(issue.Handle); handle != "" {
			entry += "@" + handle
		}
		entries = append(entries, entry)
	}
	if omitted := len(issues) - len(shown); omitted > 0 {
		entries = append(entries, "+"+strconv.Itoa(omitted)+" more")
	}
	return slog.String("issues", strings.Join(entries, "; "))
}

// Handles are block, field, relation, and locale identifiers. The cap keeps a
// malformed or caller-chosen block id from dominating the record.
func boundedHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if runes := []rune(handle); len(runes) > maxLoggedHandleRunes {
		return string(runes[:maxLoggedHandleRunes]) + "…"
	}
	return handle
}
