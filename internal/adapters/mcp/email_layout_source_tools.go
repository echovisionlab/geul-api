package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/google/uuid"
)

const (
	ToolEmailLayoutSourceGet     = "email_layout_source_get"
	ToolEmailLayoutSourceReplace = "email_layout_source_replace"
)

var emailLayoutSourceTools = toolRegistry[EmailLayoutSourceTools]{
	defineTool(oauthTool(ToolEmailLayoutSourceGet, "Get Email Layout source markup", "Read an Email Layout's source-locale wrapper markup and document_revision. layout_id is the UUID in the admin Email Layouts editor URL. The markup includes private <!--geul-unit:...--> markers that bind translations to source text; edit that text with document tools (document_type=email_layout), not here.", emailLayoutSourceGetInputJSONSchema, emailLayoutSourceOutputJSONSchema, true, false), (*EmailLayoutSourceTools).get),
	defineTool(oauthTool(ToolEmailLayoutSourceReplace, "Replace Email Layout source markup", "Change Email Layout wrapper markup such as image sizes, attributes or inline styles that document tools cannot edit. Pass the exact current document_revision and a find string that occurs exactly once in source_markup. The result is validated like an editor save and must keep every translatable unit and its source text, so all locales keep their translations. Reread with email_layout_source_get after a revision conflict; never blindly retry.", emailLayoutSourceReplaceInputJSONSchema, emailLayoutSourceReplaceOutputJSONSchema, false, false), (*EmailLayoutSourceTools).replace),
}

// EmailLayoutSourceApplication is the Email Layout document boundary that
// authorizes the editor, locks the layout and records the change.
type EmailLayoutSourceApplication interface {
	LoadSource(context.Context, string) (emailauthoring.EmailLayoutAIDocumentState, error)
	ExecuteAIDocumentMutation(
		context.Context,
		string,
		string,
		emailauthoring.EmailLayoutAIDocumentExecutionMode,
		emailauthoring.EmailLayoutAIDocumentMutationCompiler,
	) (emailauthoring.EmailLayoutAIDocumentMutationResult, error)
}

// EmailLayoutSourceTools edits the Email Layout wrapper that DCDP document
// tools deliberately leave to the source owner.
type EmailLayoutSourceTools struct {
	layouts EmailLayoutSourceApplication
}

func NewEmailLayoutSourceTools(layouts EmailLayoutSourceApplication) (*EmailLayoutSourceTools, error) {
	if interfaceValueIsNil(layouts) {
		return nil, errors.New("MCP Email Layout source application is required")
	}
	return &EmailLayoutSourceTools{layouts: layouts}, nil
}

func (*EmailLayoutSourceTools) ToolNames() []string { return emailLayoutSourceTools.names() }
func (*EmailLayoutSourceTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return emailLayoutSourceTools.list(), nil
}
func (tools *EmailLayoutSourceTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	return emailLayoutSourceTools.call(tools, ctx, name, arguments)
}

type emailLayoutSourceGetArguments struct {
	LayoutID string `json:"layout_id"`
}

type emailLayoutSourceReplaceArguments struct {
	LayoutID                 string `json:"layout_id"`
	ExpectedDocumentRevision string `json:"expected_document_revision"`
	Find                     string `json:"find"`
	Replace                  string `json:"replace"`
}

func (tools *EmailLayoutSourceTools) get(ctx context.Context, _ string, call toolCall[emailLayoutSourceGetArguments]) (mcpserver.ToolResult, error) {
	var input emailLayoutSourceGetArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := validateUUID("layout_id", input.LayoutID); err != nil {
		return executionError(err)
	}
	state, err := tools.layouts.LoadSource(ctx, input.LayoutID)
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(map[string]any{
		"layout_id": state.LayoutID, "source_locale": state.SourceLocale,
		"document_revision": state.DocumentRevision, "source_markup": state.SourceMarkup,
	})
}

func (tools *EmailLayoutSourceTools) replace(ctx context.Context, _ string, call toolCall[emailLayoutSourceReplaceArguments]) (mcpserver.ToolResult, error) {
	var input emailLayoutSourceReplaceArguments
	if err := call.Decode(&input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(call.Arguments, "find", "replace"); err != nil {
		return executionError(err)
	}
	if err := validateUUID("layout_id", input.LayoutID); err != nil {
		return executionError(err)
	}
	if err := validateUUID("expected_document_revision", input.ExpectedDocumentRevision); err != nil {
		return executionError(err)
	}
	if input.Find == "" {
		return executionError(errors.New("find must not be empty"))
	}
	source, err := tools.layouts.LoadSource(ctx, input.LayoutID)
	if err != nil {
		return expectedToolError(err)
	}
	result, err := tools.layouts.ExecuteAIDocumentMutation(ctx, input.LayoutID, source.SourceLocale, emailauthoring.EmailLayoutAIDocumentExecutionApply,
		func(state emailauthoring.EmailLayoutAIDocumentState) (emailauthoring.EmailLayoutAIDocumentMutation, error) {
			if state.DocumentRevision != input.ExpectedDocumentRevision {
				return emailauthoring.EmailLayoutAIDocumentMutation{}, &core.RevisionConflict{
					Kind: core.ConflictDocumentRevision, CurrentDocumentRevision: state.DocumentRevision,
					Message: "Email Layout document revision changed; reread with " + ToolEmailLayoutSourceGet,
				}
			}
			markup := state.SourceMarkup
			if occurrences := strings.Count(markup, input.Find); occurrences != 1 {
				return emailauthoring.EmailLayoutAIDocumentMutation{}, &core.InputError{Message: fmt.Sprintf("find must occur exactly once in source_markup; found %d", occurrences)}
			}
			contributor, err := uuid.Parse(state.ViewerMemberID)
			if err != nil {
				return emailauthoring.EmailLayoutAIDocumentMutation{}, err
			}
			return emailauthoring.EmailLayoutAIDocumentMutation{
				LayoutID: state.LayoutID, Locale: state.Locale, ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedTargetRevision: state.TargetRevision, ExpectedSource: state.SourceLocale, ExpectedPresence: state.LocaleExists,
				ContributorMemberID: contributor, ReplaceSourceMarkup: true,
				SourceMarkup: strings.Replace(markup, input.Find, input.Replace, 1),
			}, nil
		})
	if err != nil {
		if conflict, ok := errors.AsType[*core.RevisionConflict](err); ok && conflict != nil {
			return executionError(fmt.Errorf("Email Layout document revision is now %s; reread with %s and reassess the edit", conflict.CurrentDocumentRevision, ToolEmailLayoutSourceGet))
		}
		return expectedToolError(err)
	}
	return contentResult(map[string]any{"layout_id": input.LayoutID, "document_revision": result.DocumentRevision, "changed": result.Changed})
}

var _ ToolProvider = (*EmailLayoutSourceTools)(nil)
