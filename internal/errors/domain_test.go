package errors

import (
	stderrors "errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
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
		{"precondition", domainerr.Precondition("member tag is referenced by an audience segment"), FailedPrecondition("member tag is referenced by an audience segment")},
		{"denied", domainerr.Denied("avatar source does not belong to this member"), PermissionDenied("avatar source does not belong to this member")},
		{"invalid session", domainerr.InvalidSession(), InvalidSession()},
		{"unavailable", domainerr.DependencyUnavailable("account email"), DependencyUnavailable("account email")},
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

func TestClassifiedLeavesUnclassifiedErrorsAlone(t *testing.T) {
	if _, ok := Classified(stderrors.New("database is down")); ok {
		t.Fatal("an unclassified error must stay unclassified")
	}
	if _, ok := Classified(&domainerr.Error{Message: "kind is missing"}); ok {
		t.Fatal("a use-case error without a known kind must stay unclassified")
	}
}
