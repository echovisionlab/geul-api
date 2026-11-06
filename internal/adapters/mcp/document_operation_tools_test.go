package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	mcpserver "github.com/echovisionlab/geul-api/internal/mcp"
	pagedomain "github.com/echovisionlab/geul-api/internal/page"
	postdomain "github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/programevent"
	releasedomain "github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/stretchr/testify/require"
)

// recordingDocumentOperations stands for every document type's use cases and
// records which one each document reached.
type recordingDocumentOperations struct {
	PostOperationApplication
	WorkOperationApplication
	PageOperationApplication
	ReleaseOperationApplication
	ProgramEventOperationApplication
	calls     []string
	documents []string
	unchanged map[string]bool
	errs      map[string]error
	ctx       context.Context
	schedule  postdomain.SchedulePostCommand
	postSlug  postdomain.CheckSlugAvailableQuery
}

func (r *recordingDocumentOperations) record(ctx context.Context, call, document string) (changed bool, err error) {
	r.ctx = ctx
	r.calls = append(r.calls, call)
	r.documents = append(r.documents, document)
	return !r.unchanged[call], r.errs[call]
}

func (r *recordingDocumentOperations) postLifecycle(ctx context.Context, call, document string) (postdomain.Lifecycle, error) {
	_, err := r.record(ctx, call, document)
	return postdomain.Lifecycle{ID: document}, err
}

func (r *recordingDocumentOperations) PublishPost(ctx context.Context, command postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error) {
	return r.postLifecycle(ctx, "post.publish", command.ID)
}

func (r *recordingDocumentOperations) UnpublishPost(ctx context.Context, command postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error) {
	return r.postLifecycle(ctx, "post.unpublish", command.ID)
}

func (r *recordingDocumentOperations) ArchivePost(ctx context.Context, command postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error) {
	return r.postLifecycle(ctx, "post.archive", command.ID)
}

func (r *recordingDocumentOperations) RepublishPost(ctx context.Context, command postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error) {
	return r.postLifecycle(ctx, "post.republish", command.ID)
}

func (r *recordingDocumentOperations) CancelPostSchedule(ctx context.Context, command postdomain.PostLifecycleCommand) (postdomain.Lifecycle, error) {
	return r.postLifecycle(ctx, "post.cancel_schedule", command.ID)
}

func (r *recordingDocumentOperations) SchedulePost(ctx context.Context, command postdomain.SchedulePostCommand) (postdomain.Lifecycle, error) {
	r.schedule = command
	return r.postLifecycle(ctx, "post.schedule", command.ID)
}

func (r *recordingDocumentOperations) DeletePost(ctx context.Context, command postdomain.DeletePostCommand) error {
	_, err := r.record(ctx, "post.delete", command.ID)
	return err
}

func (r *recordingDocumentOperations) CheckSlugAvailable(ctx context.Context, query postdomain.CheckSlugAvailableQuery) (bool, error) {
	r.postSlug = query
	return r.record(ctx, "post.slug", query.Slug)
}

func (r *recordingDocumentOperations) PublishPage(ctx context.Context, command pagedomain.PublishPageCommand) (pagedomain.LifecycleResult, error) {
	changed, err := r.record(ctx, "page.publish", command.ID)
	return pagedomain.LifecycleResult{Changed: changed}, err
}

func (r *recordingDocumentOperations) UnpublishPage(ctx context.Context, command pagedomain.UnpublishPageCommand) (pagedomain.LifecycleResult, error) {
	changed, err := r.record(ctx, "page.unpublish", command.ID)
	return pagedomain.LifecycleResult{Changed: changed}, err
}

func (r *recordingDocumentOperations) DeletePage(ctx context.Context, command pagedomain.DeletePageCommand) error {
	_, err := r.record(ctx, "page.delete", command.ID)
	return err
}

func (r *recordingDocumentOperations) CheckPageSlugAvailable(ctx context.Context, query pagedomain.CheckPageSlugAvailableQuery) (bool, error) {
	return r.record(ctx, "page.slug", query.Slug)
}

func (r *recordingDocumentOperations) PublishWork(ctx context.Context, command work.PublishWorkCommand) (work.LifecycleChange, error) {
	changed, err := r.record(ctx, "work.publish", command.ID)
	return work.LifecycleChange{Changed: changed}, err
}

func (r *recordingDocumentOperations) UnpublishWork(ctx context.Context, command work.UnpublishWorkCommand) (work.LifecycleChange, error) {
	changed, err := r.record(ctx, "work.unpublish", command.ID)
	return work.LifecycleChange{Changed: changed}, err
}

func (r *recordingDocumentOperations) DeleteWork(ctx context.Context, command work.DeleteWorkCommand) error {
	_, err := r.record(ctx, "work.delete", command.ID)
	return err
}

func (r *recordingDocumentOperations) CheckWorkSlugAvailable(ctx context.Context, query work.CheckWorkSlugAvailableQuery) (bool, error) {
	return r.record(ctx, "work.slug", query.Slug)
}

func (r *recordingDocumentOperations) PublishRelease(ctx context.Context, command releasedomain.PublishReleaseCommand) (releasedomain.LifecycleChange, error) {
	changed, err := r.record(ctx, "release.publish", command.ID)
	return releasedomain.LifecycleChange{Changed: changed}, err
}

func (r *recordingDocumentOperations) UnpublishRelease(ctx context.Context, command releasedomain.UnpublishReleaseCommand) (releasedomain.LifecycleChange, error) {
	changed, err := r.record(ctx, "release.unpublish", command.ID)
	return releasedomain.LifecycleChange{Changed: changed}, err
}

func (r *recordingDocumentOperations) DeleteRelease(ctx context.Context, command releasedomain.DeleteReleaseCommand) error {
	_, err := r.record(ctx, "release.delete", command.ID)
	return err
}

func (r *recordingDocumentOperations) CheckReleaseSlugAvailable(ctx context.Context, query releasedomain.CheckReleaseSlugQuery) (bool, error) {
	return r.record(ctx, "release.slug", query.Slug)
}

func (r *recordingDocumentOperations) PublishProgramEvent(ctx context.Context, command programevent.PublishProgramEventCommand) (programevent.LifecycleChange, error) {
	changed, err := r.record(ctx, "program_event.publish", command.ID)
	return programevent.LifecycleChange{Changed: changed}, err
}

func (r *recordingDocumentOperations) ArchiveProgramEvent(ctx context.Context, command programevent.ArchiveProgramEventCommand) (programevent.LifecycleChange, error) {
	changed, err := r.record(ctx, "program_event.archive", command.ID)
	return programevent.LifecycleChange{Changed: changed}, err
}

func (r *recordingDocumentOperations) DeleteProgramEvent(ctx context.Context, command programevent.DeleteProgramEventCommand) error {
	_, err := r.record(ctx, "program_event.delete", command.ID)
	return err
}

// documentOperationsOver builds the tools over the given use cases, leaving the
// document types a test does not name to a recorder.
func documentOperationsOver(t *testing.T, applications DocumentOperationApplications) *DocumentOperationTools {
	t.Helper()
	unused := &recordingDocumentOperations{}
	if applications.Posts == nil {
		applications.Posts = unused
	}
	if applications.Works == nil {
		applications.Works = unused
	}
	if applications.Pages == nil {
		applications.Pages = unused
	}
	if applications.Releases == nil {
		applications.Releases = unused
	}
	if applications.Events == nil {
		applications.Events = unused
	}
	tools, err := NewDocumentOperationTools(applications)
	require.NoError(t, err)
	return tools
}

func documentOperationFixture(t *testing.T) (*DocumentOperationTools, *recordingDocumentOperations) {
	t.Helper()
	operations := &recordingDocumentOperations{}
	return documentOperationsOver(t, DocumentOperationApplications{Posts: operations, Works: operations, Pages: operations, Releases: operations, Events: operations}), operations
}

func batchOutput(t *testing.T, result mcpserver.ToolResult) (documents []documentOperationResult, applied, failed int) {
	t.Helper()
	var output struct {
		Documents []documentOperationResult `json:"documents"`
		Applied   int                       `json:"applied"`
		Failed    int                       `json:"failed"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content[0]["text"].(string)), &output))
	return output.Documents, output.Applied, output.Failed
}

const (
	operationDocumentA = "11111111-1111-4111-8111-111111111111"
	operationDocumentB = "22222222-2222-4222-8222-222222222222"
	operationDocumentC = "33333333-3333-4333-8333-333333333333"
)

func TestDocumentOperationToolsNeedEveryApplication(t *testing.T) {
	operations := &recordingDocumentOperations{}
	var typedNil *recordingDocumentOperations
	complete := DocumentOperationApplications{Posts: operations, Works: operations, Pages: operations, Releases: operations, Events: operations}
	for name, breakIt := range map[string]func(*DocumentOperationApplications){
		"posts":     func(a *DocumentOperationApplications) { a.Posts = nil },
		"works":     func(a *DocumentOperationApplications) { a.Works = nil },
		"pages":     func(a *DocumentOperationApplications) { a.Pages = nil },
		"releases":  func(a *DocumentOperationApplications) { a.Releases = nil },
		"events":    func(a *DocumentOperationApplications) { a.Events = nil },
		"typed nil": func(a *DocumentOperationApplications) { a.Posts = typedNil },
	} {
		t.Run(name, func(t *testing.T) {
			applications := complete
			breakIt(&applications)
			_, err := NewDocumentOperationTools(applications)
			require.Error(t, err)
		})
	}
}

// Every action a document type has must reach that type's own use case.
func TestDocumentPublishSendsEachActionToItsUseCase(t *testing.T) {
	for _, test := range []struct{ action, documentType, call string }{
		{"publish", "post", "post.publish"}, {"publish", "page", "page.publish"}, {"publish", "work", "work.publish"},
		{"publish", "release", "release.publish"}, {"publish", "program_event", "program_event.publish"},
		{"unpublish", "post", "post.unpublish"}, {"unpublish", "page", "page.unpublish"}, {"unpublish", "work", "work.unpublish"},
		{"unpublish", "release", "release.unpublish"},
		{"archive", "post", "post.archive"}, {"archive", "program_event", "program_event.archive"},
		{"republish", "post", "post.republish"}, {"cancel_schedule", "post", "post.cancel_schedule"},
	} {
		t.Run(test.action+"/"+test.documentType, func(t *testing.T) {
			tools, operations := documentOperationFixture(t)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"`+test.action+`","documents":[{"document_type":"`+test.documentType+`","document_id":"`+operationDocumentA+`"}]}`))
			require.NoError(t, err)
			documents, applied, failed := batchOutput(t, result)
			require.Equal(t, 1, applied)
			require.Zero(t, failed)
			require.True(t, *documents[0].Changed)
			require.Equal(t, []string{test.call}, operations.calls)
			require.Equal(t, []string{operationDocumentA}, operations.documents)
		})
	}
}

func TestDocumentPublishReportsADocumentThatDidNotChange(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	operations.unchanged = map[string]bool{"page.publish": true}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"publish","documents":[{"document_type":"page","document_id":"`+operationDocumentA+`"}]}`))
	require.NoError(t, err)
	documents, applied, _ := batchOutput(t, result)
	require.Equal(t, 1, applied)
	require.False(t, *documents[0].Changed)
}

// One refused document must neither hide what the others did nor be lost.
func TestDocumentPublishReportsEachDocumentSeparately(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	operations.errs = map[string]error{"page.publish": connect.NewError(connect.CodeFailedPrecondition, errors.New("the page has no slug"))}

	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"publish","documents":[
		{"document_type":"post","document_id":"`+operationDocumentA+`"},
		{"document_type":"page","document_id":"`+operationDocumentB+`"}]}`))

	require.NoError(t, err)
	require.False(t, result.IsError, "a partly applied call is not a failed call")
	documents, applied, failed := batchOutput(t, result)
	require.Equal(t, 1, applied)
	require.Equal(t, 1, failed)
	require.Equal(t, documentStatusFailed, documents[1].Status)
	require.Equal(t, "the page has no slug", documents[1].Error)
}

func TestDocumentPublishIsAnErrorOnlyWhenNothingApplied(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	operations.errs = map[string]error{"page.publish": connect.NewError(connect.CodeFailedPrecondition, errors.New("refused"))}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"publish","documents":[{"document_type":"page","document_id":"`+operationDocumentA+`"}]}`))
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func TestDocumentOperationKeepsServerFailuresOutOfTheResult(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	operations.errs = map[string]error{"post.publish": errors.New("pq: secret connection detail")}
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"publish","documents":[{"document_type":"post","document_id":"`+operationDocumentA+`"}]}`))
	require.NoError(t, err)
	documents, _, _ := batchOutput(t, result)
	require.NotContains(t, documents[0].Error, "secret")
}

// The use case decides who may act. Its refusal must reach the caller, under the
// caller's own context.
func TestDocumentOperationsKeepTheUseCaseAuthority(t *testing.T) {
	type contextKey struct{}
	for code, want := range map[connect.Code]string{
		connect.CodeUnauthenticated:    "owning decision",
		connect.CodePermissionDenied:   "owning decision",
		connect.CodeNotFound:           "owning decision",
		connect.CodeInvalidArgument:    "owning decision",
		connect.CodeFailedPrecondition: "owning decision",
		connect.CodeAborted:            "owning decision",
		connect.CodeUnavailable:        "The service is temporarily unavailable",
		connect.CodeInternal:           "the operation failed on the server; nothing changed in this document",
	} {
		t.Run(code.String(), func(t *testing.T) {
			tools, operations := documentOperationFixture(t)
			operations.errs = map[string]error{"release.delete": connect.NewError(code, errors.New("owning decision"))}
			ctx := context.WithValue(t.Context(), contextKey{}, "authenticated actor")

			result, err := tools.CallTool(ctx, mcpserver.Principal{}, ToolDocumentDelete, toolArguments(t, `{"documents":[{"document_type":"release","document_id":"`+operationDocumentA+`"}]}`))

			require.NoError(t, err)
			require.True(t, result.IsError)
			documents, _, failed := batchOutput(t, result)
			require.Equal(t, 1, failed)
			require.Equal(t, want, documents[0].Error)
			require.Equal(t, "authenticated actor", operations.ctx.Value(contextKey{}))
		})
	}
}

func TestDocumentPublishSchedulesWithItsTime(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"schedule","scheduled_at":"2026-12-01T00:00:00Z","scheduled_time_zone":"Asia/Seoul","documents":[{"document_type":"post","document_id":"`+operationDocumentA+`"}]}`))
	require.NoError(t, err)
	require.Equal(t, operationDocumentA, operations.schedule.ID)
	require.Equal(t, "2026-12-01T00:00:00Z", operations.schedule.ScheduledAt.UTC().Format("2006-01-02T15:04:05Z"))
	require.Equal(t, "Asia/Seoul", operations.schedule.TimeZone)
}

// Refusing before any write keeps a call from half applying because of a mistake
// visible in its arguments.
func TestDocumentPublishRefusesBadCallsBeforeTouchingAnything(t *testing.T) {
	documents := `"documents":[{"document_type":"post","document_id":"` + operationDocumentA + `"},{"document_type":"page","document_id":"` + operationDocumentB + `"}]`
	for name, arguments := range map[string]string{
		"unknown action":         `{"action":"launch",` + documents + `}`,
		"delete is its own tool": `{"action":"delete",` + documents + `}`,
		"action a type lacks":    `{"action":"archive",` + documents + `}`,
		"schedule without time":  `{"action":"schedule",` + documents + `}`,
		"time without schedule":  `{"action":"publish","scheduled_at":"2026-12-01T00:00:00Z",` + documents + `}`,
		"schedule with bad time": `{"action":"schedule","scheduled_at":"tomorrow","scheduled_time_zone":"Asia/Seoul",` + documents + `}`,
		"no documents":           `{"action":"publish","documents":[]}`,
		"document without UUID":  `{"action":"publish","documents":[{"document_type":"post","document_id":"` + operationDocumentA + `"},{"document_type":"post","document_id":"a-slug"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			tools, operations := documentOperationFixture(t)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, arguments))
			require.Error(t, err)
			require.Empty(t, operations.calls)
		})
	}
}

func TestDocumentPublishBoundsOneCall(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	entries := make([]string, documentBatchMaxDocuments+1)
	for index := range entries {
		entries[index] = `{"document_type":"post","document_id":"` + operationDocumentA + `"}`
	}
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentPublish, toolArguments(t, `{"action":"publish","documents":[`+strings.Join(entries, ",")+`]}`))
	require.Error(t, err)
	require.Empty(t, operations.calls)
}

func TestDocumentDeleteIsMarkedDestructiveAndReachesEachType(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentDelete, toolArguments(t, `{"documents":[
		{"document_type":"post","document_id":"`+operationDocumentA+`"},
		{"document_type":"page","document_id":"`+operationDocumentA+`"},
		{"document_type":"work","document_id":"`+operationDocumentA+`"},
		{"document_type":"release","document_id":"`+operationDocumentB+`"},
		{"document_type":"program_event","document_id":"`+operationDocumentC+`"}]}`))
	require.NoError(t, err)
	documents, applied, _ := batchOutput(t, result)
	require.Equal(t, 5, applied)
	require.True(t, *documents[0].Changed)
	require.Equal(t, []string{"post.delete", "page.delete", "work.delete", "release.delete", "program_event.delete"}, operations.calls)

	listed, err := tools.ListTools(t.Context(), mcpserver.Principal{})
	require.NoError(t, err)
	hints := map[string]any{}
	for _, tool := range listed {
		hints[tool.Name] = tool.Annotations["destructiveHint"]
	}
	require.Equal(t, true, hints[ToolDocumentDelete])
	require.Equal(t, false, hints[ToolDocumentPublish], "publishing must not carry a delete's warning")
}

func TestDocumentSettingsGetRefusesWhatItCannotRead(t *testing.T) {
	tools, _ := documentOperationFixture(t)
	for name, arguments := range map[string]string{
		"a type without settings": `{"document_type":"menu","document_id":"` + operationDocumentA + `"}`,
		"a slug for an ID":        `{"document_type":"release","document_id":"album"}`,
		"a field of no tool":      `{"document_type":"post","document_id":"` + operationDocumentA + `","locale":"en"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSettingsGet, toolArguments(t, arguments))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
		})
	}
}

func TestDocumentSlugCheckReachesEachType(t *testing.T) {
	for documentType, call := range map[string]string{"post": "post.slug", "page": "page.slug", "work": "work.slug", "release": "release.slug"} {
		t.Run(documentType, func(t *testing.T) {
			tools, operations := documentOperationFixture(t)
			result, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSlugCheck, toolArguments(t, `{"document_type":"`+documentType+`","slug":"album"}`))
			require.NoError(t, err)
			require.Equal(t, []string{call}, operations.calls)
			require.Equal(t, []string{"album"}, operations.documents)
			require.Equal(t, true, result.StructuredContent["available"])
			require.Equal(t, documentType, result.StructuredContent["document_type"])
		})
	}
}

func TestDocumentSlugCheckPassesTheDocumentToExclude(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSlugCheck, toolArguments(t, `{"document_type":"post","slug":"album","exclude_document_id":"`+operationDocumentA+`"}`))
	require.NoError(t, err)
	require.Equal(t, operationDocumentA, *operations.postSlug.ExcludePostID)
}

// An empty slug is the use case's to judge.
func TestDocumentSlugCheckKeepsAnExplicitEmptySlug(t *testing.T) {
	tools, operations := documentOperationFixture(t)
	_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSlugCheck, toolArguments(t, `{"document_type":"release","slug":""}`))
	require.NoError(t, err)
	require.Equal(t, []string{""}, operations.documents)
}

func TestDocumentSlugCheckRefusesInvalidInputBeforeTheUseCase(t *testing.T) {
	for name, arguments := range map[string]string{
		"no slug":           `{"document_type":"release"}`,
		"null slug":         `{"document_type":"release","slug":null}`,
		"null exclude":      `{"document_type":"release","slug":"a","exclude_document_id":null}`,
		"exclude a slug":    `{"document_type":"release","slug":"a","exclude_document_id":"not-a-uuid"}`,
		"type with no slug": `{"document_type":"menu","slug":"a"}`,
	} {
		t.Run(name, func(t *testing.T) {
			tools, operations := documentOperationFixture(t)
			_, err := tools.CallTool(t.Context(), mcpserver.Principal{}, ToolDocumentSlugCheck, toolArguments(t, arguments))
			var execution *mcpserver.ToolExecutionError
			require.ErrorAs(t, err, &execution)
			require.Empty(t, operations.calls)
		})
	}
}
