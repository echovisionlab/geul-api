package errors

import (
	stderrors "errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"google.golang.org/protobuf/proto"
)

// A use case that switches from an errs constructor to its domainerr
// counterpart must keep the client's code and message.
func TestDomainErrorsMapToTheirConnectCounterparts(t *testing.T) {
	cases := []struct {
		name    string
		domain  *domainerr.Error
		connect *connect.Error
	}{
		{"invalid field", domainerr.InvalidField("nickname", "must be between 1 and 100 characters"), InvalidArgument("nickname", "must be between 1 and 100 characters")},
		{"required", domainerr.Required("name"), Required("name")},
		{"not found", domainerr.ResourceNotFound("member", "id-1"), NotFound("member", "id-1")},
		{"duplicate", domainerr.Duplicate("member", "nickname", "taken"), AlreadyExists("member", "nickname", "taken")},
		{"precondition", domainerr.Precondition("member tag is referenced by a Member criteria"), FailedPrecondition("member tag is referenced by a Member criteria")},
		{"denied", domainerr.Denied("avatar source does not belong to this member"), PermissionDenied("avatar source does not belong to this member")},
		{"invalid session", domainerr.InvalidSession(), InvalidSession()},
		{"unavailable", domainerr.DependencyUnavailable("account email"), DependencyUnavailable("account email")},
		{"authentication required", domainerr.AuthenticationRequired(), AuthenticationRequired()},
		{"account banned", domainerr.AccountBanned(), AccountBanned()},
		{"no permission", domainerr.NoPermission("edit", "post"), NoPermission("edit", "post")},
		{"admin required", domainerr.AdminRequired(), AdminRequired()},
		{"author required", domainerr.AuthorRequired(), AuthorRequired()},
		{"invalid entity type", domainerr.InvalidEntityType("SHARE_LINK_ENTITY_TYPE_UNSPECIFIED"), InvalidEntityType("SHARE_LINK_ENTITY_TYPE_UNSPECIFIED")},
		{"invalid entity id", domainerr.InvalidEntityID(), InvalidEntityID()},
		{"retry", domainerr.Retry("Member criteria changed while it was being archived; retry"), connect.NewError(connect.CodeAborted, stderrors.New("Member criteria changed while it was being archived; retry"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := Wrap(tc.domain)
			if mapped.Code() != tc.connect.Code() || mapped.Message() != tc.connect.Message() {
				t.Fatalf("Wrap(%v) = %s %q, want %s %q", tc.domain, mapped.Code(), mapped.Message(), tc.connect.Code(), tc.connect.Message())
			}
		})
	}
}

func TestDomainErrorCauseStaysServerSide(t *testing.T) {
	cause := stderrors.New("duplicate key value violates unique constraint member_nickname_key")
	err := fmt.Errorf("update member profile: %w", domainerr.Duplicate("member", "nickname", "taken").WithCause(cause))

	mapped := Wrap(err)
	if mapped.Code() != connect.CodeAlreadyExists || mapped.Message() != "member with nickname 'taken' already exists" {
		t.Fatalf("Wrap() = %s %q", mapped.Code(), mapped.Message())
	}
	if !stderrors.Is(mapped, cause) {
		t.Fatal("mapped error lost the cause")
	}
	if operation := failurecause.Message(mapped); operation == mapped.Message() {
		t.Fatalf("failure record lost the operation context: %q", operation)
	}
	if level := OperationLogLevel(err); level != slog.LevelWarn {
		t.Fatalf("OperationLogLevel() = %s, want WARN", level)
	}
}

// A use case that masks a denial as not found keeps the denial as its cause;
// the client sees the outer answer.
func TestClassifiedAnswersTheOutermostClassification(t *testing.T) {
	denied := NoPermission("manage_share_links", "label")
	err := fmt.Errorf("create share link: %w", domainerr.ResourceNotFound("label", "id-1").WithCause(denied))

	mapped := Wrap(err)
	if mapped.Code() != connect.CodeNotFound || mapped.Message() != "label not found: id-1" {
		t.Fatalf("Wrap() = %s %q", mapped.Code(), mapped.Message())
	}
	if !stderrors.Is(mapped, denied) {
		t.Fatal("mapped error lost the masked denial")
	}

	masked := fmt.Errorf("lock: %w", WithCause(NotFound("menu", "id-2"), domainerr.NoPermission("edit", "menu")))
	if mapped := Wrap(masked); mapped.Code() != connect.CodeNotFound {
		t.Fatalf("Wrap() = %s, want the outer Connect classification", mapped.Code())
	}
	joined := stderrors.Join(stderrors.New("rollback failed"), domainerr.Precondition("menu is locked"))
	if mapped := Wrap(joined); mapped.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("Wrap() = %s, want the joined use-case classification", mapped.Code())
	}
}

func TestCodeOfReadsUseCaseOutcomes(t *testing.T) {
	if code := CodeOf(fmt.Errorf("lock: %w", domainerr.NoPermission("edit", "menu"))); code != connect.CodePermissionDenied {
		t.Fatalf("CodeOf() = %s, want permission_denied", code)
	}
	if code := CodeOf(NotFound("menu", "id")); code != connect.CodeNotFound {
		t.Fatalf("CodeOf() = %s, want not_found", code)
	}
	if code := CodeOf(stderrors.New("database is down")); code != connect.CodeUnknown {
		t.Fatalf("CodeOf() = %s, want unknown", code)
	}
}

// A collaboration conflict decided by a use case reaches the client exactly
// as the Connect constructor builds it.
func TestDomainCollaborationOutcomesKeepTheirDetail(t *testing.T) {
	exists := true
	domain := Wrap(domainerr.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, "target changed",
		domainerr.CollaborationRevisions{Document: "document-1", Target: "target-1", TargetExists: &exists},
	))
	legacy := CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_TARGET_REVISION_CHANGED, "target changed",
		CollaborationState{Document: "document-1", Target: "target-1", TargetExists: &exists},
	)
	requireSameClientError(t, legacy, domain)

	// A conflict found while storing reports the revisions its cause carries,
	// as the Connect constructor read them from its causes.
	for _, cause := range []error{
		fmt.Errorf("store: %w", CollaborationState{Document: "document-2", TargetExists: &exists}),
		stderrors.New("store failed without revisions"),
	} {
		stale := intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_DOCUMENT_REVISION_CHANGED
		requireSameClientError(t,
			CollaborationConflict(stale, "document changed", cause),
			Wrap(domainerr.CollaborationConflict(stale, "document changed", domainerr.CollaborationRevisionsOf(cause))),
		)
	}

	rejection := intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN
	requireSameClientError(t, CollaborationMutationRejection(rejection, "structure is shared"), Wrap(domainerr.CollaborationRejection(rejection, "structure is shared")))
}

func requireSameClientError(t *testing.T, want, got *connect.Error) {
	t.Helper()
	if got.Code() != want.Code() || got.Message() != want.Message() || len(got.Details()) != len(want.Details()) {
		t.Fatalf("got %s %q with %d details, want %s %q with %d", got.Code(), got.Message(), len(got.Details()), want.Code(), want.Message(), len(want.Details()))
	}
	for index := range want.Details() {
		wantValue, err := want.Details()[index].Value()
		if err != nil {
			t.Fatal(err)
		}
		gotValue, err := got.Details()[index].Value()
		if err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(wantValue, gotValue) {
			t.Fatalf("detail %d = %v, want %v", index, gotValue, wantValue)
		}
	}
}

func TestClassifiedLeavesUnclassifiedErrorsAlone(t *testing.T) {
	if _, ok := Classified(stderrors.New("database is down")); ok {
		t.Fatal("an unclassified error must stay unclassified")
	}
	if _, ok := Classified(&domainerr.Error{Message: "kind is missing"}); ok {
		t.Fatal("a use-case error without a known kind must stay unclassified")
	}
}
