package aidocument

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssuesLogAttrOmitsMessagesAndBoundsHandles(t *testing.T) {
	longHandle := "block:" + strings.Repeat("x", 100)
	attr := IssuesLogAttr([]OperationIssue{
		{Operation: 0, Code: IssueUnknownField, Message: "private document text"},
		{Operation: 3, Code: IssueUnknownBlock, Handle: longHandle, Message: "private document text"},
	})

	require.Equal(t, "issues", attr.Key)
	value := attr.Value.String()
	require.NotContains(t, value, "private")
	require.Equal(t, "op0:unknown_field; op3:unknown_block@"+longHandle[:maxLoggedHandleRunes]+"…", value)
}
