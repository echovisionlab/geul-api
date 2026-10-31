// Package targetbatchtest drives the shared target-locale batch validator
// through one rejection table per domain. Each domain table pins the client
// messages its entity name produces, so a change to the shared rules shows up
// as an explicit expectation diff in every domain.
package targetbatchtest

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
)

// Validator is the signature every domain's target batch validator shares.
type Validator func(batch contentblock.Batch, documentID, expectedRevision uuid.UUID, locale string, allowLocaleDeletes bool) error

// Kind names one input shape a target-locale batch can take.
type Kind string

const (
	DocumentMismatch     Kind = "document_mismatch"
	RevisionMismatch     Kind = "revision_mismatch"
	GraphUpsert          Kind = "graph_upsert"
	GraphDelete          Kind = "graph_delete"
	GraphReorder         Kind = "graph_reorder"
	MultipleLocaleGroups Kind = "multiple_locale_groups"
	LocaleMismatch       Kind = "locale_mismatch"
	LocaleDelete         Kind = "locale_delete"
	LocaleDeleteAllowed  Kind = "locale_delete_allowed"
	LocaleUpsert         Kind = "locale_upsert"
	Empty                Kind = "empty"
)

// Kinds lists every input shape; each domain table must cover all of them.
var Kinds = []Kind{
	DocumentMismatch, RevisionMismatch, GraphUpsert, GraphDelete, GraphReorder,
	MultipleLocaleGroups, LocaleMismatch, LocaleDelete, LocaleDeleteAllowed,
	LocaleUpsert, Empty,
}

// Outcome is the client-visible result of one validation. The zero value
// means the batch was accepted.
type Outcome struct {
	Code    connect.Code
	Message string
	// Reason is the CollaborationMutationRejectionDetail reason, or
	// UNSPECIFIED when the error carries no rejection detail.
	Reason intrav1.CollaborationMutationRejectionReason
}

// Case is one row of a domain table.
type Case struct {
	Kind Kind
	Want Outcome
}

// Accepted is the outcome of a batch the validator lets through.
func Accepted() Outcome { return Outcome{} }

// Invalid is an InvalidArgument without a rejection reason.
func Invalid(message string) Outcome {
	return Outcome{Code: connect.CodeInvalidArgument, Message: message}
}

// Rejected is an InvalidArgument carrying a CollaborationMutationRejectionDetail.
func Rejected(reason intrav1.CollaborationMutationRejectionReason, message string) Outcome {
	return Outcome{Code: connect.CodeInvalidArgument, Message: message, Reason: reason}
}

const (
	NonSourceStructureForbidden = intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN
	RoomLocaleMismatch          = intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH
)

var (
	documentID = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c35")
	revision   = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c36")
	blockID    = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c37")
)

const (
	locale      = "ko"
	otherLocale = "ja"
)

type input struct {
	batch        contentblock.Batch
	allowDeletes bool
}

func build(kind Kind) input {
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: revision}
	update := contentblock.LocaleBlockUpdate{BlockID: blockID, LocalizedData: []byte(`{}`)}
	allowDeletes := false
	switch kind {
	case DocumentMismatch:
		batch.DocumentID = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c38")
	case RevisionMismatch:
		batch.ExpectedRevision = uuid.MustParse("019c89aa-6798-7a37-8532-11e03f729c39")
	case GraphUpsert:
		batch.Upserts = []contentblock.BaseBlock{{ID: blockID}}
	case GraphDelete:
		batch.Deletes = []uuid.UUID{blockID}
	case GraphReorder:
		batch.Reorders = []contentblock.Reorder{{BlockID: blockID}}
	case MultipleLocaleGroups:
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{
			{Locale: locale, Upserts: []contentblock.LocaleBlockUpdate{update}},
			{Locale: locale, Upserts: []contentblock.LocaleBlockUpdate{update}},
		}
	case LocaleMismatch:
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{{Locale: otherLocale, Upserts: []contentblock.LocaleBlockUpdate{update}}}
	case LocaleDelete:
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{{Locale: locale, Deletes: []uuid.UUID{blockID}}}
	case LocaleDeleteAllowed:
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{{Locale: locale, Deletes: []uuid.UUID{blockID}}}
		allowDeletes = true
	case LocaleUpsert:
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{{Locale: locale, Upserts: []contentblock.LocaleBlockUpdate{update}}}
	case Empty:
	}
	return input{batch: batch, allowDeletes: allowDeletes}
}

// Run feeds every Kind to validate and compares the outcome with the table.
func Run(t *testing.T, validate Validator, cases []Case) {
	t.Helper()
	seen := make(map[Kind]bool, len(cases))
	for _, tc := range cases {
		if seen[tc.Kind] {
			t.Fatalf("duplicate case %q", tc.Kind)
		}
		seen[tc.Kind] = true
		t.Run(string(tc.Kind), func(t *testing.T) {
			in := build(tc.Kind)
			got := Describe(t, validate(in.batch, documentID, revision, locale, in.allowDeletes))
			if got != tc.Want {
				t.Fatalf("outcome = %+v, want %+v", got, tc.Want)
			}
		})
	}
	for _, kind := range Kinds {
		if !seen[kind] {
			t.Errorf("table does not cover %q", kind)
		}
	}
}

// Describe reduces a validator error to its client-visible Outcome.
func Describe(t *testing.T, err error) Outcome {
	t.Helper()
	if err == nil {
		return Accepted()
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		t.Fatalf("error %T is not a connect error: %v", err, err)
	}
	outcome := Outcome{Code: connectErr.Code(), Message: connectErr.Message()}
	for _, detail := range connectErr.Details() {
		value, detailErr := detail.Value()
		if detailErr != nil {
			t.Fatalf("decode error detail: %v", detailErr)
		}
		rejection, ok := value.(*intrav1.CollaborationMutationRejectionDetail)
		if !ok {
			t.Fatalf("unexpected error detail %T", value)
		}
		if outcome.Reason != 0 {
			t.Fatal("error carries more than one rejection detail")
		}
		outcome.Reason = rejection.GetReason()
	}
	return outcome
}

// Shared is the shared contentblock validator speaking for one domain's
// entity name.
func Shared(entity string) Validator {
	return func(batch contentblock.Batch, documentID, expectedRevision uuid.UUID, locale string, allowLocaleDeletes bool) error {
		return contentblock.ValidateTargetLocaleBatch(entity, batch, documentID, expectedRevision, locale, allowLocaleDeletes)
	}
}
