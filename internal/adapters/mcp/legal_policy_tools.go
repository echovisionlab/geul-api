package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

type TermsPolicyManagementApplication interface {
	ListTermsVersions(context.Context, *connect.Request[managev1.ListTermsVersionsRequest]) (*connect.Response[managev1.ListTermsVersionsResponse], error)
	GetTermsVersion(context.Context, *connect.Request[managev1.GetTermsVersionRequest]) (*connect.Response[managev1.Terms], error)
	CreateTermsVersion(context.Context, *connect.Request[managev1.CreateTermsVersionRequest]) (*connect.Response[managev1.Terms], error)
	ScheduleTerms(context.Context, *connect.Request[managev1.ScheduleTermsRequest]) (*connect.Response[managev1.TermsLifecycleMutationResponse], error)
	CancelTermsSchedule(context.Context, *connect.Request[managev1.CancelTermsScheduleRequest]) (*connect.Response[managev1.TermsLifecycleMutationResponse], error)
	ActivateTermsNow(context.Context, *connect.Request[managev1.ActivateTermsNowRequest]) (*connect.Response[managev1.TermsLifecycleMutationResponse], error)
	DeleteTerms(context.Context, *connect.Request[managev1.DeleteTermsRequest]) (*connect.Response[managev1.DeleteResponse], error)
}

type PrivacyPolicyManagementApplication interface {
	ListPrivacyVersions(context.Context, *connect.Request[managev1.ListPrivacyVersionsRequest]) (*connect.Response[managev1.ListPrivacyVersionsResponse], error)
	GetPrivacyVersion(context.Context, *connect.Request[managev1.GetPrivacyVersionRequest]) (*connect.Response[managev1.Privacy], error)
	CreatePrivacyVersion(context.Context, *connect.Request[managev1.CreatePrivacyVersionRequest]) (*connect.Response[managev1.Privacy], error)
	SchedulePrivacy(context.Context, *connect.Request[managev1.SchedulePrivacyRequest]) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error)
	CancelPrivacySchedule(context.Context, *connect.Request[managev1.CancelPrivacyScheduleRequest]) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error)
	ActivatePrivacyNow(context.Context, *connect.Request[managev1.ActivatePrivacyNowRequest]) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error)
	DeletePrivacy(context.Context, *connect.Request[managev1.DeletePrivacyRequest]) (*connect.Response[managev1.DeleteResponse], error)
}

// LegalPolicyTools delegates all lifecycle, audit, notice delivery and deletion
// decisions to the same Legal applications used by the site. Document editing
// continues through AIDocumentTools and its interactive mutation coordinator.
type LegalPolicyTools struct {
	terms   TermsPolicyManagementApplication
	privacy PrivacyPolicyManagementApplication
}

func NewLegalPolicyTools(terms TermsPolicyManagementApplication, privacy PrivacyPolicyManagementApplication) (*LegalPolicyTools, error) {
	if interfaceValueIsNil(terms) || interfaceValueIsNil(privacy) {
		return nil, errors.New("MCP Terms and Privacy policy applications are required")
	}
	return &LegalPolicyTools{terms: terms, privacy: privacy}, nil
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
	var effectiveFrom *timestamppb.Timestamp
	if name == ToolPolicySchedule {
		instant, err := time.Parse(time.RFC3339Nano, input.EffectiveFrom)
		if err != nil {
			return executionError(errors.New("effective_from must be an RFC 3339 instant"))
		}
		effectiveFrom = timestamppb.New(instant)
		if err := effectiveFrom.CheckValid(); err != nil {
			return executionError(err)
		}
	}
	if input.DocumentType == "terms" {
		return tools.callTerms(ctx, name, input, effectiveFrom)
	}
	return tools.callPrivacy(ctx, name, input, effectiveFrom)
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
	var output map[string]any
	if input.DocumentType == "terms" {
		response, err := tools.terms.CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{Title: input.Title}))
		if err != nil {
			return expectedToolError(err)
		}
		output = termsPolicyOutput(response.Msg)
	} else {
		response, err := tools.privacy.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{Title: input.Title}))
		if err != nil {
			return expectedToolError(err)
		}
		output = privacyPolicyOutput(response.Msg)
	}
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
	pagination := &commonv1.PaginationRequest{Limit: limit, Offset: input.Offset}
	policies := make([]map[string]any, 0)
	var page *commonv1.PaginationResponse
	if input.DocumentType == "terms" {
		request := &managev1.ListTermsVersionsRequest{Pagination: pagination}
		if input.Status != nil {
			status := managev1.TermsStatus(managev1.TermsStatus_value["TERMS_STATUS_"+strings.ToUpper(*input.Status)])
			request.Status = &status
		}
		response, err := tools.terms.ListTermsVersions(ctx, connect.NewRequest(request))
		if err != nil {
			return expectedToolError(err)
		}
		for _, policy := range response.Msg.Versions {
			policies = append(policies, termsPolicyOutput(policy))
		}
		page = response.Msg.Pagination
	} else {
		request := &managev1.ListPrivacyVersionsRequest{Pagination: pagination}
		if input.Status != nil {
			status := managev1.PrivacyStatus(managev1.PrivacyStatus_value["PRIVACY_STATUS_"+strings.ToUpper(*input.Status)])
			request.Status = &status
		}
		response, err := tools.privacy.ListPrivacyVersions(ctx, connect.NewRequest(request))
		if err != nil {
			return expectedToolError(err)
		}
		for _, policy := range response.Msg.Versions {
			policies = append(policies, privacyPolicyOutput(policy))
		}
		page = response.Msg.Pagination
	}
	var nextOffset any
	if page.GetHasMore() {
		nextOffset = int64(page.GetOffset()) + int64(page.GetLimit())
	}
	return contentResult(map[string]any{"policies": policies, "total": page.GetTotal(), "next_offset": nextOffset})
}

func (tools *LegalPolicyTools) callTerms(ctx context.Context, name string, input policyArguments, effectiveFrom *timestamppb.Timestamp) (mcpserver.ToolResult, error) {
	if name == ToolPolicyGet {
		response, err := tools.terms.GetTermsVersion(ctx, connect.NewRequest(&managev1.GetTermsVersionRequest{Id: input.DocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(termsPolicyOutput(response.Msg))
	}
	if name == ToolPolicyDelete {
		response, err := tools.terms.DeleteTerms(ctx, connect.NewRequest(&managev1.DeleteTermsRequest{Id: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "terms", "document_id": input.DocumentID, "changed": response.Msg.Success, "deleted": response.Msg.Success})
	}
	var response *connect.Response[managev1.TermsLifecycleMutationResponse]
	var err error
	switch name {
	case ToolPolicySchedule:
		response, err = tools.terms.ScheduleTerms(ctx, connect.NewRequest(&managev1.ScheduleTermsRequest{Id: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision, EffectiveFrom: effectiveFrom}))
	case ToolPolicyScheduleCancel:
		response, err = tools.terms.CancelTermsSchedule(ctx, connect.NewRequest(&managev1.CancelTermsScheduleRequest{Id: input.DocumentID}))
	case ToolPolicyActivateNow:
		response, err = tools.terms.ActivateTermsNow(ctx, connect.NewRequest(&managev1.ActivateTermsNowRequest{Id: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(policyLifecycleOutput("terms", response.Msg.Id, response.Msg.Changed, response.Msg.DocumentRevision, contentStatus(response.Msg.Status.String(), "TERMS_STATUS_"), response.Msg.EffectiveFrom, response.Msg.EffectiveUntil, response.Msg.UpdatedAt))
}

func termsPolicyOutput(policy *managev1.Terms) map[string]any {
	return map[string]any{"document_type": "terms", "document_id": policy.Id, "version": policy.Version, "title": policy.Title, "source_locale": policy.SourceLocale, "document_revision": policy.Revision, "status": contentStatus(policy.Status.String(), "TERMS_STATUS_"), "effective_from": optionalProgramEventTimestamp(policy.EffectiveFrom), "effective_until": optionalProgramEventTimestamp(policy.EffectiveUntil), "created_at": timestampString(policy.CreatedAt), "updated_at": timestampString(policy.UpdatedAt)}
}

func (tools *LegalPolicyTools) callPrivacy(ctx context.Context, name string, input policyArguments, effectiveFrom *timestamppb.Timestamp) (mcpserver.ToolResult, error) {
	if name == ToolPolicyGet {
		response, err := tools.privacy.GetPrivacyVersion(ctx, connect.NewRequest(&managev1.GetPrivacyVersionRequest{Id: input.DocumentID}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(privacyPolicyOutput(response.Msg))
	}
	if name == ToolPolicyDelete {
		response, err := tools.privacy.DeletePrivacy(ctx, connect.NewRequest(&managev1.DeletePrivacyRequest{Id: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision}))
		if err != nil {
			return expectedToolError(err)
		}
		return contentResult(map[string]any{"document_type": "privacy", "document_id": input.DocumentID, "changed": response.Msg.Success, "deleted": response.Msg.Success})
	}
	var response *connect.Response[managev1.PrivacyLifecycleMutationResponse]
	var err error
	switch name {
	case ToolPolicySchedule:
		response, err = tools.privacy.SchedulePrivacy(ctx, connect.NewRequest(&managev1.SchedulePrivacyRequest{Id: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision, EffectiveFrom: effectiveFrom}))
	case ToolPolicyScheduleCancel:
		response, err = tools.privacy.CancelPrivacySchedule(ctx, connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{Id: input.DocumentID}))
	case ToolPolicyActivateNow:
		response, err = tools.privacy.ActivatePrivacyNow(ctx, connect.NewRequest(&managev1.ActivatePrivacyNowRequest{Id: input.DocumentID, ExpectedRevision: input.ExpectedDocumentRevision}))
	}
	if err != nil {
		return expectedToolError(err)
	}
	return contentResult(policyLifecycleOutput("privacy", response.Msg.Id, response.Msg.Changed, response.Msg.DocumentRevision, contentStatus(response.Msg.Status.String(), "PRIVACY_STATUS_"), response.Msg.EffectiveFrom, response.Msg.EffectiveUntil, response.Msg.UpdatedAt))
}

func privacyPolicyOutput(policy *managev1.Privacy) map[string]any {
	return map[string]any{"document_type": "privacy", "document_id": policy.Id, "version": policy.Version, "title": policy.Title, "source_locale": policy.SourceLocale, "document_revision": policy.Revision, "status": contentStatus(policy.Status.String(), "PRIVACY_STATUS_"), "effective_from": optionalProgramEventTimestamp(policy.EffectiveFrom), "effective_until": optionalProgramEventTimestamp(policy.EffectiveUntil), "created_at": timestampString(policy.CreatedAt), "updated_at": timestampString(policy.UpdatedAt)}
}

func policyLifecycleOutput(kind, id string, changed bool, revision, status string, effectiveFrom, effectiveUntil, updatedAt *timestamppb.Timestamp) map[string]any {
	return map[string]any{"document_type": kind, "document_id": id, "changed": changed, "document_revision": revision, "status": status, "effective_from": optionalProgramEventTimestamp(effectiveFrom), "effective_until": optionalProgramEventTimestamp(effectiveUntil), "updated_at": timestampString(updatedAt)}
}

var _ ToolProvider = (*LegalPolicyTools)(nil)
