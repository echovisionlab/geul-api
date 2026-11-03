package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/google/uuid"
)

const testLayoutID = "11111111-1111-4111-8111-111111111111"

// fakeEmailLayoutSource runs the compiler against a fixed source state, the
// way the Email Layout service does inside its transaction.
type fakeEmailLayoutSource struct {
	revision string
	markup   string
	compiled *emailauthoring.EmailLayoutAIDocumentMutation
}

func (f *fakeEmailLayoutSource) state() emailauthoring.EmailLayoutAIDocumentState {
	return emailauthoring.EmailLayoutAIDocumentState{
		LayoutID: testLayoutID, DocumentRevision: f.revision, SourceLocale: "en", Locale: "en",
		LocaleExists: true, ViewerMemberID: uuid.NewString(), SourceMarkup: f.markup,
	}
}

func (f *fakeEmailLayoutSource) LoadSource(context.Context, string) (emailauthoring.EmailLayoutAIDocumentState, error) {
	return f.state(), nil
}

func (f *fakeEmailLayoutSource) ExecuteAIDocumentMutation(_ context.Context, _ string, locale string, _ emailauthoring.EmailLayoutAIDocumentExecutionMode, compiler emailauthoring.EmailLayoutAIDocumentMutationCompiler) (emailauthoring.EmailLayoutAIDocumentMutationResult, error) {
	if locale != "en" {
		return emailauthoring.EmailLayoutAIDocumentMutationResult{}, errors.New("source locale was not used")
	}
	compiled, err := compiler(f.state())
	if err != nil {
		return emailauthoring.EmailLayoutAIDocumentMutationResult{}, err
	}
	f.compiled = &compiled
	return emailauthoring.EmailLayoutAIDocumentMutationResult{DocumentRevision: uuid.NewString(), Changed: true}, nil
}

func TestEmailLayoutSourceReplaceChangesOneExactMatchAtTheReadRevision(t *testing.T) {
	revision := uuid.NewString()
	source := &fakeEmailLayoutSource{revision: revision, markup: `<img src="{{logo_email_url}}" width="72" height="72"><p>{{content}}</p>`}
	tools, err := NewEmailLayoutSourceTools(source)
	if err != nil {
		t.Fatal(err)
	}
	read, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolEmailLayoutSourceGet, toolArguments(t, `{"layout_id":"`+testLayoutID+`"}`))
	if err != nil || read.StructuredContent["source_markup"] != source.markup || read.StructuredContent["document_revision"] != revision {
		t.Fatalf("get = %#v, %v", read.StructuredContent, err)
	}

	call := func(find, revision string) error {
		_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolEmailLayoutSourceReplace, toolArguments(t,
			`{"layout_id":"`+testLayoutID+`","expected_document_revision":"`+revision+`","find":`+find+`,"replace":"width=\"60\""}`))
		return err
	}
	var execution *mcpserver.ToolExecutionError
	if err := call(`"72"`, revision); !errors.As(err, &execution) || !strings.Contains(execution.Message, "found 2") {
		t.Fatalf("an ambiguous find must not change markup: %v", err)
	}
	if err := call(`"width=\"72\" height=\"72\""`, uuid.NewString()); !errors.As(err, &execution) || !strings.Contains(execution.Message, revision) {
		t.Fatalf("a stale revision must report the current one: %v", err)
	}
	if source.compiled != nil {
		t.Fatal("rejected calls must not compile a mutation")
	}
	if err := call(`"width=\"72\" height=\"72\""`, revision); err != nil {
		t.Fatal(err)
	}
	if !source.compiled.ReplaceSourceMarkup || source.compiled.SourceMarkup != `<img src="{{logo_email_url}}" width="60"><p>{{content}}</p>` {
		t.Fatalf("compiled = %#v", source.compiled)
	}
}
