package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/legal"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ToolPolicyList           = "policy_list"
	ToolPolicyGet            = "policy_get"
	ToolPolicyCreate         = "policy_create"
	ToolPolicySchedule       = "policy_schedule"
	ToolPolicyScheduleCancel = "policy_schedule_cancel"
	ToolPolicyActivateNow    = "policy_activate_now"
	ToolPolicyDelete         = "policy_delete"
)

var legalPolicyTools = []mcpserver.Tool{
	oauthTool(ToolPolicyList, "List legal policies", "List authorized Terms of Service or Privacy Policy versions, optionally filtered by lifecycle status. Use returned document_id UUIDs with policy_get or document_open/read (p=terms or p=privacy).", policyListInputJSONSchema, policyListOutputJSONSchema, true, false),
	oauthTool(ToolPolicyGet, "Get legal policy", "Read legal policy metadata, lifecycle and current document_revision without returning the body. Read the body with document_open/read using the returned source_locale. Source title and body edits are allowed in every lifecycle with the required permissions; use document_metadata_update and document paragraph/block tools. Editing sends no notice email.", policyIDInputJSONSchema, policyOutputJSONSchema, true, false),
	oauthTool(ToolPolicyCreate, "Create legal policy version", "Create a new draft Terms of Service or Privacy Policy version with an empty typed document. Creation sends no notice email. The existing service selects source_locale from the caller preference and site defaults; use the returned locale for document tools. Fill title/body with document_metadata_update and document paragraph/block tools, then explicitly schedule or activate to start notice delivery.", policyCreateInputJSONSchema, policyOutputJSONSchema, false, false),
	oauthTool(ToolPolicySchedule, "Schedule legal policy", "Schedule a draft legal policy using its exact current document_revision as expected_document_revision and an RFC 3339 effective_from instant. This starts the existing notice email flow: advance notice is due seven days before effect (or immediately when closer), followed by an effective notice at activation. Reread and reassess after stale revisions; never blindly retry.", policyScheduleInputJSONSchema, policyMutationOutputJSONSchema, false, true),
	oauthTool(ToolPolicyScheduleCancel, "Cancel legal policy schedule", "Return a scheduled legal policy to draft through the existing service. Cancellation is rejected if notice delivery has already started; delivery history is preserved. This does not send a new notice email.", policyIDInputJSONSchema, policyMutationOutputJSONSchema, false, true),
	oauthTool(ToolPolicyActivateNow, "Activate legal policy now", "Activate a draft or scheduled legal policy immediately with its exact current document_revision as expected_document_revision. This archives the previous active version and starts the existing effective notice email flow. Reread and reassess after stale revisions; never blindly retry.", policyRevisionInputJSONSchema, policyMutationOutputJSONSchema, false, true),
	oauthTool(ToolPolicyDelete, "Delete legal policy", "Permanently delete a legal policy in any lifecycle using its exact current document_revision as expected_document_revision, including versions with notice delivery history. Sealed delivery history is retained; outstanding notices and pending recipients are cancelled. The public route and OG image are refreshed. This sends no new notice email.", policyRevisionInputJSONSchema, policyMutationOutputJSONSchema, false, true),
}

// LegalPolicyManagementApplication is the Terms and Privacy management use
// cases the tools call, for the kind each call names.
type LegalPolicyManagementApplication interface {
	ListPolicyVersions(context.Context, legal.PolicyKind, legal.ListPolicyVersionsQuery) (legal.PolicyPage, error)
	GetPolicyVersion(context.Context, legal.PolicyKind, legal.GetPolicyVersionQuery) (legal.Policy, error)
	CreatePolicyVersion(context.Context, legal.PolicyKind, legal.CreatePolicyVersionCommand) (legal.Policy, error)
	SchedulePolicy(context.Context, legal.PolicyKind, legal.SchedulePolicyCommand) (legal.PolicyLifecycle, error)
	CancelPolicySchedule(context.Context, legal.PolicyKind, legal.CancelPolicyScheduleCommand) (legal.PolicyLifecycle, error)
	ActivatePolicyNow(context.Context, legal.PolicyKind, legal.ActivatePolicyNowCommand) (legal.PolicyLifecycle, error)
	DeletePolicy(context.Context, legal.PolicyKind, legal.DeletePolicyCommand) error
}

// LegalPolicyTools delegates all lifecycle, audit, notice delivery and deletion
// decisions to the same Legal applications used by the site. Document editing
// continues through AIDocumentTools and its interactive mutation coordinator.
type LegalPolicyTools struct {
	policies LegalPolicyManagementApplication
}

func NewLegalPolicyTools(policies LegalPolicyManagementApplication) (*LegalPolicyTools, error) {
	if interfaceValueIsNil(policies) {
		return nil, errors.New("MCP Terms and Privacy policy applications are required")
	}
	return &LegalPolicyTools{policies: policies}, nil
}
func (*LegalPolicyTools) ToolNames() []string { return toolDefinitionNames(legalPolicyTools) }
func (*LegalPolicyTools) ListTools(context.Context, mcpserver.Principal) ([]mcpserver.Tool, error) {
	return cloneToolDefinitions(legalPolicyTools), nil
}

type policyArguments struct {
	DocumentType             string `json:"document_type"`
	DocumentID               string `json:"document_id"`
	ExpectedDocumentRevision string `json:"expected_document_revision"`
	EffectiveFrom            string `json:"effective_from"`
}

func (tools *LegalPolicyTools) CallTool(ctx context.Context, _ mcpserver.Principal, name string, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	switch name {
	case ToolPolicyCreate:
		return tools.create(ctx, arguments)
	case ToolPolicyList:
		return tools.list(ctx, arguments)
	case ToolPolicyGet, ToolPolicySchedule, ToolPolicyScheduleCancel, ToolPolicyActivateNow, ToolPolicyDelete:
	default:
		return mcpserver.ToolResult{}, mcpserver.ErrUnknownTool
	}
	// Decode only the advertised arguments for each command, including runtime
	// validation for clients that do not enforce the JSON schema.
	allowed := map[string]bool{"document_type": true, "document_id": true}
	if name == ToolPolicySchedule || name == ToolPolicyActivateNow || name == ToolPolicyDelete {
		allowed["expected_document_revision"] = true
	}
	if name == ToolPolicySchedule {
		allowed["effective_from"] = true
	}
	for field := range arguments {
		if !allowed[field] {
			return executionError(fmt.Errorf("unexpected argument %q", field))
		}
	}
	var input policyArguments
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := validatePolicyType(input.DocumentType); err != nil {
		return executionError(err)
	}
	if err := validateUUID("document_id", input.DocumentID); err != nil {
		return executionError(err)
	}
	if allowed["expected_document_revision"] {
		if err := validateUUID("expected_document_revision", input.ExpectedDocumentRevision); err != nil {
			return executionError(err)
		}
	}
	var effectiveFrom *time.Time
	if name == ToolPolicySchedule {
		instant, err := time.Parse(time.RFC3339Nano, input.EffectiveFrom)
		if err != nil {
			return mcpserver.ToolResult{}, &mcpserver.ToolExecutionError{Message: "effective_from must be an RFC 3339 instant", Cause: err}
		}
		if err := timestamppb.New(instant).CheckValid(); err != nil {
			return executionError(err)
		}
		effectiveFrom = &instant
	}
	return tools.callPolicy(ctx, name, legal.PolicyKind(input.DocumentType), input, effectiveFrom)
}

func validatePolicyType(value string) error {
	if value != "terms" && value != "privacy" {
		return errors.New("document_type must be terms or privacy")
	}
	return nil
}

func (tools *LegalPolicyTools) create(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		DocumentType string  `json:"document_type"`
		Title        *string `json:"title,omitempty"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "title"); err != nil {
		return executionError(err)
	}
	if err := validatePolicyType(input.DocumentType); err != nil {
		return executionError(err)
	}
	if input.Title != nil && strings.TrimSpace(*input.Title) == "" {
		return executionError(errors.New("title must not be blank"))
	}
	kind := legal.PolicyKind(input.DocumentType)
	created, err := tools.policies.CreatePolicyVersion(ctx, kind, legal.CreatePolicyVersionCommand{Title: input.Title})
	if err != nil {
		return expectedToolError(err)
	}
	output := policyOutput(kind, created)
	output["changed"] = true
	return contentResult(output)
}

func (tools *LegalPolicyTools) list(ctx context.Context, arguments mcpserver.ToolArguments) (mcpserver.ToolResult, error) {
	var input struct {
		DocumentType string  `json:"document_type"`
		Status       *string `json:"status,omitempty"`
		Limit        *int32  `json:"limit,omitempty"`
		Offset       int32   `json:"offset,omitempty"`
	}
	if err := decodeArguments(arguments, &input); err != nil {
		return executionError(err)
	}
	if err := rejectNullArguments(arguments, "status", "limit", "offset"); err != nil {
		return executionError(err)
	}
	if err := validatePolicyType(input.DocumentType); err != nil {
		return executionError(err)
	}
	limit := int32(20)
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 50 || input.Offset < 0 {
		return executionError(errors.New("limit must be 1..50 and offset must be non-negative"))
	}
	if input.Status != nil {
		switch *input.Status {
		case "draft", "scheduled", "active", "archived":
		default:
			return executionError(errors.New("status must be draft, scheduled, active, or archived"))
		}
	}
	kind := legal.PolicyKind(input.DocumentType)
	query := legal.ListPolicyVersionsQuery{Limit: limit, Offset: input.Offset}
	if input.Status != nil {
		status, _ := legal.PolicyStatus(kind, *input.Status)
		query.Status = &status
	}
	listed, err := tools.policies.ListPolicyVersions(ctx, kind, query)
	if err != nil {
		return expectedToolError(err)
	}
	policies := make([]map[string]any, 0, len(listed.Versions))
	for _, policy := range listed.Versions {
		policies = append(policies, policyOutput(kind, policy))
	}
	page := listed.Pagination
	var nextOffset any
	if page.GetHasMore() {
		nextOffset = int64(page.GetOffset()) + int64(page.GetLimit())
	}
	return contentResult(map[string]any{"policies": policies, "total": page.GetTotal(), "next_offset": nextOffset})
}

func (tools *LegalPolicyTools) callPolicy(ctx context.Context, name string, kind legal.PolicyKind, input policyArguments, effectiveFrom *time.Time) (mcpserver.ToolResult, error) {
	if name == ToolPolicyGet {
		policy, err := tools.policies.GetPolicyVersion(ctx, kind, legal.GetPolicyVersionQuery{ID: input.DocumentID})
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(policyOutput(kind, policy))
	}
	if name == ToolPolicyDelete {
		if err := tools.policies.DeletePolicy(ctx, kind, legal.DeletePolicyCommand{ID: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision}); err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": string(kind), "document_id": input.DocumentID, "changed": true, "deleted": true})
	}
	var lifecycle legal.PolicyLifecycle
	var err error
	switch name {
	case ToolPolicySchedule:
		lifecycle, err = tools.policies.SchedulePolicy(ctx, kind, legal.SchedulePolicyCommand{ID: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision, EffectiveFrom: effectiveFrom})
	case ToolPolicyScheduleCancel:
		lifecycle, err = tools.policies.CancelPolicySchedule(ctx, kind, legal.CancelPolicyScheduleCommand{ID: input.DocumentID})
	case ToolPolicyActivateNow:
		lifecycle, err = tools.policies.ActivatePolicyNow(ctx, kind, legal.ActivatePolicyNowCommand{ID: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision})
	}
	if err != nil {
		return expectedToolError(err)
	}
	if kind == legal.Terms {
		response := legal.TermsLifecycleMessage(lifecycle)
		return contentResult(policyLifecycleOutput(string(kind), response.Id, response.Changed, response.DocumentRevision, contentStatus(response.Status.String(), "TERMS_STATUS_"), response.EffectiveFrom, response.EffectiveUntil, response.UpdatedAt))
	}
	response := legal.PrivacyLifecycleMessage(lifecycle)
	return contentResult(policyLifecycleOutput(string(kind), response.Id, response.Changed, response.DocumentRevision, contentStatus(response.Status.String(), "PRIVACY_STATUS_"), response.EffectiveFrom, response.EffectiveUntil, response.UpdatedAt))
}

// policyOutput renders a version as the Connect message of its kind would,
// so tool output follows the API's status names.
func policyOutput(kind legal.PolicyKind, policy legal.Policy) map[string]any {
	if kind == legal.Terms {
		message := legal.TermsMessage(policy)
		return policyMessageOutput(string(kind), message.Id, message.Version, message.Title, message.SourceLocale, message.Revision, contentStatus(message.Status.String(), "TERMS_STATUS_"), message.EffectiveFrom, message.EffectiveUntil, message.CreatedAt, message.UpdatedAt)
	}
	message := legal.PrivacyMessage(policy)
	return policyMessageOutput(string(kind), message.Id, message.Version, message.Title, message.SourceLocale, message.Revision, contentStatus(message.Status.String(), "PRIVACY_STATUS_"), message.EffectiveFrom, message.EffectiveUntil, message.CreatedAt, message.UpdatedAt)
}

func policyMessageOutput(kind, id string, version int32, title, sourceLocale, revision, status string, effectiveFrom, effectiveUntil, createdAt, updatedAt *timestamppb.Timestamp) map[string]any {
	return map[string]any{"document_type": kind, "document_id": id, "version": version, "title": title, "source_locale": sourceLocale, "document_revision": revision, "status": status, "effective_from": optionalProgramEventTimestamp(effectiveFrom), "effective_until": optionalProgramEventTimestamp(effectiveUntil), "created_at": timestampString(createdAt), "updated_at": timestampString(updatedAt)}
}

func policyLifecycleOutput(kind, id string, changed bool, revision, status string, effectiveFrom, effectiveUntil, updatedAt *timestamppb.Timestamp) map[string]any {
	return map[string]any{"document_type": kind, "document_id": id, "changed": changed, "document_revision": revision, "status": status, "effective_from": optionalProgramEventTimestamp(effectiveFrom), "effective_until": optionalProgramEventTimestamp(effectiveUntil), "updated_at": timestampString(updatedAt)}
}

var _ ToolProvider = (*LegalPolicyTools)(nil)
