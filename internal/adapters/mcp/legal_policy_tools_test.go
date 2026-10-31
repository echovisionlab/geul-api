package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/legal"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const policyTestID = "11111111-1111-4111-8111-111111111111"
const policyTestRevision = "22222222-2222-4222-8222-222222222222"

type recordingLegalPolicies struct {
	calls   []string
	request proto.Message
	err     error
}

func (owner *recordingLegalPolicies) record(name string, request proto.Message) error {
	owner.calls = append(owner.calls, name)
	owner.request = request
	return owner.err
}
func policyTestPolicy(kind legal.PolicyKind) legal.Policy {
	status := model.TermsStatusDraft
	if kind == legal.Privacy {
		status = model.PrivacyStatusDraft
	}
	now := time.Now()
	return legal.Policy{ID: policyTestID, Version: 2, Title: "Policy title", SourceLocale: "ko", Revision: policyTestRevision, Status: status, Document: &contentv1.RichTextDocument{SourceLocale: "body must not leak"}, CreatedAt: now, UpdatedAt: now}
}

func policyTestLifecycle(kind legal.PolicyKind) legal.PolicyLifecycle {
	status := model.TermsStatusScheduled
	if kind == legal.Privacy {
		status = model.PrivacyStatusScheduled
	}
	return legal.PolicyLifecycle{ID: policyTestID, Changed: true, DocumentRevision: policyTestRevision, Status: status, UpdatedAt: time.Now()}
}

func policyTestTimestamp(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

// The recorder keeps the Connect request each use-case call corresponds
// to, so the assertions read like the RPCs the site sends.
func (owner *recordingLegalPolicies) ListPolicyVersions(_ context.Context, kind legal.PolicyKind, query legal.ListPolicyVersionsQuery) (legal.PolicyPage, error) {
	pagination := &commonv1.PaginationRequest{Limit: query.Limit, Offset: query.Offset}
	var err error
	if kind == legal.Terms {
		request := &managev1.ListTermsVersionsRequest{Pagination: pagination}
		if query.Status != nil {
			status := legal.TermsStatusToProto(*query.Status)
			request.Status = &status
		}
		err = owner.record("ListTermsVersions", request)
	} else {
		request := &managev1.ListPrivacyVersionsRequest{Pagination: pagination}
		if query.Status != nil {
			status := legal.PrivacyStatusToProto(*query.Status)
			request.Status = &status
		}
		err = owner.record("ListPrivacyVersions", request)
	}
	if err != nil {
		return legal.PolicyPage{}, err
	}
	return legal.PolicyPage{Versions: []legal.Policy{policyTestPolicy(kind)}, Pagination: &commonv1.PaginationResponse{Total: 4, Limit: query.Limit, Offset: query.Offset, HasMore: true}}, nil
}
func (owner *recordingLegalPolicies) GetPolicyVersion(_ context.Context, kind legal.PolicyKind, query legal.GetPolicyVersionQuery) (legal.Policy, error) {
	var err error
	if kind == legal.Terms {
		err = owner.record("GetTermsVersion", &managev1.GetTermsVersionRequest{Id: query.ID})
	} else {
		err = owner.record("GetPrivacyVersion", &managev1.GetPrivacyVersionRequest{Id: query.ID})
	}
	if err != nil {
		return legal.Policy{}, err
	}
	return policyTestPolicy(kind), nil
}
func (owner *recordingLegalPolicies) CreatePolicyVersion(_ context.Context, kind legal.PolicyKind, command legal.CreatePolicyVersionCommand) (legal.Policy, error) {
	var err error
	if kind == legal.Terms {
		err = owner.record("CreateTermsVersion", &managev1.CreateTermsVersionRequest{Title: command.Title, Document: command.Document})
	} else {
		err = owner.record("CreatePrivacyVersion", &managev1.CreatePrivacyVersionRequest{Title: command.Title, Document: command.Document})
	}
	if err != nil {
		return legal.Policy{}, err
	}
	return policyTestPolicy(kind), nil
}
func (owner *recordingLegalPolicies) SchedulePolicy(_ context.Context, kind legal.PolicyKind, command legal.SchedulePolicyCommand) (legal.PolicyLifecycle, error) {
	var err error
	if kind == legal.Terms {
		err = owner.record("ScheduleTerms", &managev1.ScheduleTermsRequest{Id: command.ID, ExpectedRevision: command.ExpectedRevision, EffectiveFrom: policyTestTimestamp(command.EffectiveFrom)})
	} else {
		err = owner.record("SchedulePrivacy", &managev1.SchedulePrivacyRequest{Id: command.ID, ExpectedRevision: command.ExpectedRevision, EffectiveFrom: policyTestTimestamp(command.EffectiveFrom)})
	}
	if err != nil {
		return legal.PolicyLifecycle{}, err
	}
	return policyTestLifecycle(kind), nil
}
func (owner *recordingLegalPolicies) CancelPolicySchedule(_ context.Context, kind legal.PolicyKind, command legal.CancelPolicyScheduleCommand) (legal.PolicyLifecycle, error) {
	var err error
	if kind == legal.Terms {
		err = owner.record("CancelTermsSchedule", &managev1.CancelTermsScheduleRequest{Id: command.ID})
	} else {
		err = owner.record("CancelPrivacySchedule", &managev1.CancelPrivacyScheduleRequest{Id: command.ID})
	}
	if err != nil {
		return legal.PolicyLifecycle{}, err
	}
	return policyTestLifecycle(kind), nil
}
func (owner *recordingLegalPolicies) ActivatePolicyNow(_ context.Context, kind legal.PolicyKind, command legal.ActivatePolicyNowCommand) (legal.PolicyLifecycle, error) {
	var err error
	if kind == legal.Terms {
		err = owner.record("ActivateTermsNow", &managev1.ActivateTermsNowRequest{Id: command.ID, ExpectedRevision: command.ExpectedRevision})
	} else {
		err = owner.record("ActivatePrivacyNow", &managev1.ActivatePrivacyNowRequest{Id: command.ID, ExpectedRevision: command.ExpectedRevision})
	}
	if err != nil {
		return legal.PolicyLifecycle{}, err
	}
	return policyTestLifecycle(kind), nil
}
func (owner *recordingLegalPolicies) DeletePolicy(_ context.Context, kind legal.PolicyKind, command legal.DeletePolicyCommand) error {
	if kind == legal.Terms {
		return owner.record("DeleteTerms", &managev1.DeleteTermsRequest{Id: command.ID, ExpectedRevision: command.ExpectedRevision})
	}
	return owner.record("DeletePrivacy", &managev1.DeletePrivacyRequest{Id: command.ID, ExpectedRevision: command.ExpectedRevision})
}

func TestLegalPolicyToolsCatalog(t *testing.T) {
	_, err := NewLegalPolicyTools(nil)
	require.Error(t, err)
	var missing *recordingLegalPolicies
	_, err = NewLegalPolicyTools(missing)
	require.Error(t, err)
	owner := &recordingLegalPolicies{}
	tools, err := NewLegalPolicyTools(owner)
	require.NoError(t, err)
	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Len(t, listed, 7)
	require.Equal(t, toolDefinitionNames(listed), tools.ToolNames())
	for _, tool := range listed {
		assertMCPToolOAuthSecurity(t, tool)
		for _, schema := range []json.RawMessage{tool.InputSchema, tool.OutputSchema} {
			var value map[string]any
			require.NoError(t, json.Unmarshal(schema, &value))
			require.Equal(t, "object", value["type"])
			require.Equal(t, false, value["additionalProperties"])
		}
	}
	require.Contains(t, listed[2].Description, "Creation sends no notice email")
	require.Contains(t, listed[3].Description, "seven days before")
	require.Contains(t, listed[5].Description, "effective notice email")
	require.Contains(t, listed[6].Description, "including versions with notice delivery history")
	listed[0].InputSchema[0] = '['
	listed[0].Annotations["readOnlyHint"] = false
	again, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	require.Equal(t, byte('{'), again[0].InputSchema[0])
	require.Equal(t, true, again[0].Annotations["readOnlyHint"])
}

func TestLegalPolicyDiscoveryAndCreationUseOwnersWithoutLifecycleSideEffects(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			owner := &recordingLegalPolicies{}
			tools, _ := NewLegalPolicyTools(owner)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPolicyCreate, toolArguments(t, `{"document_type":"`+kind+`","title":"새 정책"}`))
			require.NoError(t, err)
			require.Equal(t, true, result.StructuredContent["changed"])
			require.Equal(t, policyTestRevision, result.StructuredContent["document_revision"])
			require.Equal(t, "ko", result.StructuredContent["source_locale"])
			// Creation must never implicitly schedule or activate a version.
			require.Len(t, owner.calls, 1)
			if kind == "terms" {
				require.Equal(t, "CreateTermsVersion", owner.calls[0])
				require.Equal(t, "새 정책", owner.request.(*managev1.CreateTermsVersionRequest).GetTitle())
			} else {
				require.Equal(t, "CreatePrivacyVersion", owner.calls[0])
				require.Equal(t, "새 정책", owner.request.(*managev1.CreatePrivacyVersionRequest).GetTitle())
			}
			result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPolicyGet, toolArguments(t, `{"document_type":"`+kind+`","document_id":"`+policyTestID+`"}`))
			require.NoError(t, err)
			encoded, err := json.Marshal(result.StructuredContent)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), "body must not leak")
			require.NotContains(t, string(encoded), "snapshot_digest")
			require.Equal(t, kind, result.StructuredContent["document_type"])
			result, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolPolicyList, toolArguments(t, `{"document_type":"`+kind+`","status":"archived","limit":1,"offset":2}`))
			require.NoError(t, err)
			require.EqualValues(t, 3, result.StructuredContent["next_offset"])
			require.EqualValues(t, 4, result.StructuredContent["total"])
			if kind == "terms" {
				request := owner.request.(*managev1.ListTermsVersionsRequest)
				require.Equal(t, managev1.TermsStatus_TERMS_STATUS_ARCHIVED, request.GetStatus())
				require.Equal(t, int32(2), request.Pagination.Offset)
			} else {
				request := owner.request.(*managev1.ListPrivacyVersionsRequest)
				require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED, request.GetStatus())
				require.Equal(t, int32(2), request.Pagination.Offset)
			}
		})
	}
}

func TestLegalPolicyLifecycleForwardsExactRevisionAndInstant(t *testing.T) {
	instant := timestamppb.New(time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC))
	for _, kind := range []string{"terms", "privacy"} {
		for _, name := range []string{ToolPolicySchedule, ToolPolicyScheduleCancel, ToolPolicyActivateNow, ToolPolicyDelete} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				owner := &recordingLegalPolicies{}
				tools, _ := NewLegalPolicyTools(owner)
				arguments := toolArguments(t, `{"document_type":"`+kind+`","document_id":"`+policyTestID+`"}`)
				if name != ToolPolicyScheduleCancel {
					arguments["expected_document_revision"], _ = json.Marshal(policyTestRevision)
				}
				if name == ToolPolicySchedule {
					arguments["effective_from"] = json.RawMessage(`"2026-10-20T19:30:00+09:00"`)
				}
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, arguments)
				require.NoError(t, err)
				require.Len(t, owner.calls, 1)
				require.Equal(t, true, result.StructuredContent["changed"])
				var expected proto.Message
				if kind == "terms" {
					switch name {
					case ToolPolicySchedule:
						expected = &managev1.ScheduleTermsRequest{Id: policyTestID, ExpectedRevision: policyTestRevision, EffectiveFrom: instant}
					case ToolPolicyScheduleCancel:
						expected = &managev1.CancelTermsScheduleRequest{Id: policyTestID}
					case ToolPolicyActivateNow:
						expected = &managev1.ActivateTermsNowRequest{Id: policyTestID, ExpectedRevision: policyTestRevision}
					case ToolPolicyDelete:
						expected = &managev1.DeleteTermsRequest{Id: policyTestID, ExpectedRevision: policyTestRevision}
					}
				}
				if kind == "privacy" {
					switch name {
					case ToolPolicySchedule:
						expected = &managev1.SchedulePrivacyRequest{Id: policyTestID, ExpectedRevision: policyTestRevision, EffectiveFrom: instant}
					case ToolPolicyScheduleCancel:
						expected = &managev1.CancelPrivacyScheduleRequest{Id: policyTestID}
					case ToolPolicyActivateNow:
						expected = &managev1.ActivatePrivacyNowRequest{Id: policyTestID, ExpectedRevision: policyTestRevision}
					case ToolPolicyDelete:
						expected = &managev1.DeletePrivacyRequest{Id: policyTestID, ExpectedRevision: policyTestRevision}
					}
				}
				require.True(t, proto.Equal(expected, owner.request), "got %v, want %v", owner.request, expected)
				if name == ToolPolicyDelete {
					require.Equal(t, true, result.StructuredContent["deleted"])
				} else {
					require.Equal(t, policyTestRevision, result.StructuredContent["document_revision"])
				}
			})
		}
	}
}

func TestLegalPolicyRejectsMalformedArgumentsBeforeCallingOwner(t *testing.T) {
	for _, test := range []struct{ name, input string }{
		{ToolPolicyCreate, `{"document_type":"post"}`},
		{ToolPolicyCreate, `{"document_type":"terms","title":null}`},
		{ToolPolicyCreate, `{"document_type":"terms","title":"  "}`},
		{ToolPolicyCreate, `{"document_type":"terms","send_email":true}`},
		{ToolPolicyGet, `{"document_type":"terms","document_id":"slug"}`},
		{ToolPolicyList, `{"document_type":"privacy","status":"published"}`},
		{ToolPolicyList, `{"document_type":"privacy","limit":0}`},
		{ToolPolicyList, `{"document_type":"privacy","limit":51}`},
		{ToolPolicyList, `{"document_type":"privacy","offset":-1}`},
		{ToolPolicyList, `{"document_type":"privacy","limit":null}`},
		{ToolPolicyDelete, `{"document_type":"terms","document_id":"` + policyTestID + `"}`},
		{ToolPolicyActivateNow, `{"document_type":"terms","document_id":"` + policyTestID + `","expected_document_revision":"stale-text"}`},
		{ToolPolicySchedule, `{"document_type":"terms","document_id":"` + policyTestID + `","expected_document_revision":"` + policyTestRevision + `","effective_from":"2026-10-20"}`},
		{ToolPolicyScheduleCancel, `{"document_type":"terms","document_id":"` + policyTestID + `","effective_from":"2026-10-20T00:00:00Z"}`},
	} {
		t.Run(test.name+test.input, func(t *testing.T) {
			owner := &recordingLegalPolicies{}
			tools, _ := NewLegalPolicyTools(owner)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, test.name, toolArguments(t, test.input))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Empty(t, owner.calls)
		})
	}
	tools, _ := NewLegalPolicyTools(&recordingLegalPolicies{})
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, "policy_unknown", nil)
	require.ErrorIs(t, err, mcpserver.ErrUnknownTool)
}

func TestLegalPolicyPreservesOwnerDenialAndLifecycleGuardsWithoutRetry(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		for _, name := range []string{ToolPolicyGet, ToolPolicyCreate, ToolPolicyList, ToolPolicySchedule, ToolPolicyScheduleCancel, ToolPolicyActivateNow, ToolPolicyDelete} {
			for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeFailedPrecondition, connect.CodeAborted} {
				owner := &recordingLegalPolicies{err: connect.NewError(code, errors.New("owner rejected the operation"))}
				tools, _ := NewLegalPolicyTools(owner)
				args := toolArguments(t, `{"document_type":"`+kind+`"}`)
				if name != ToolPolicyCreate && name != ToolPolicyList {
					args["document_id"], _ = json.Marshal(policyTestID)
				}
				if name == ToolPolicySchedule || name == ToolPolicyActivateNow || name == ToolPolicyDelete {
					args["expected_document_revision"], _ = json.Marshal(policyTestRevision)
				}
				if name == ToolPolicySchedule {
					args["effective_from"] = json.RawMessage(`"2026-10-20T00:00:00Z"`)
				}
				result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, name, args)
				var execution *mcpserver.ToolExecutionError
				require.ErrorAs(t, err, &execution)
				require.Equal(t, "owner rejected the operation", execution.Message)
				require.Len(t, owner.calls, 1)
				require.Nil(t, result.StructuredContent)
			}
		}
	}
}

func TestLegalPolicyTitleUsesDocumentMutationCoordinator(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		application := &recordingAIDocumentApplication{applyResult: core.ApplyResult{DocumentRevision: "new-revision", Changed: true}}
		tools := mustAIDocumentTools(t, application)
		args := toolArguments(t, `{"document_type":"`+kind+`","document_id":"`+policyTestID+`","locale":"ko","expected_document_revision":"`+policyTestRevision+`","title":"수정된 정책"}`)
		result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, args)
		require.NoError(t, err)
		require.Equal(t, "new-revision", result.StructuredContent["dr"])
		request := application.applyRequest
		require.Equal(t, core.Domain(kind), request.Profile)
		require.Equal(t, core.Revision(policyTestRevision), request.ExpectedDocumentRevision)
		require.Len(t, request.Operations, 1)
		require.Equal(t, "수정된 정책", request.Operations[0].SetField.Value.Text)
		require.Equal(t, core.BlockID("document"), request.Operations[0].SetField.Target.Block)
		require.Equal(t, core.FieldID("title"), request.Operations[0].SetField.Target.Field)
		// Reject unsupported summary rather than partially applying the title.
		args["summary"] = json.RawMessage(`"unsupported"`)
		_, err = tools.CallTool(t.Context(), mcpserver.Principal{}, ToolMetadataUpdate, args)
		require.Error(t, err)
		require.Equal(t, 1, application.applyCalls)
		var schema struct {
			Properties struct {
				DocumentType struct {
					Enum []string `json:"enum"`
				} `json:"document_type"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal([]byte(documentMetadataUpdateInputJSONSchema), &schema))
		require.Contains(t, schema.Properties.DocumentType.Enum, kind)
	}
}
