package domainaudit

import (
	"context"
	"testing"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type recordingAppender struct {
	record sharedtelemetry.AuditRecord
	called bool
}

func (w *recordingAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	w.record = record
	w.called = true
	return nil
}

func TestAppendVersionPreservesAuthenticatedSystemActor(t *testing.T) {
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(),
		sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceEditorCollab},
	)
	if err != nil {
		t.Fatal(err)
	}
	writer := &recordingAppender{}
	err = AppendVersion(
		sharedtelemetry.WithRequestContext(t.Context(), requestContext),
		nil,
		writer,
		sharedtelemetry.AuditPostUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostVersionCreatedAuditRecord(
				metadata,
				"post-1",
				"version-1",
				[]string{"1b6bcad2-c90d-49e9-bec7-f9a4ba6b2894"},
			)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !writer.called {
		t.Fatal("audit writer was not called")
	}
	if writer.record.Kind != sharedtelemetry.ActorKindSystem || writer.record.Service != string(sharedtelemetry.ServiceEditorCollab) {
		t.Fatalf("version audit actor = %#v, want geul-collab system", writer.record.RecordActor)
	}
}

func TestAppendVersionRejectsMissingRequestContext(t *testing.T) {
	writer := &recordingAppender{}
	err := AppendVersion(
		t.Context(),
		nil,
		writer,
		sharedtelemetry.AuditPostUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostVersionCreatedAuditRecord(
				metadata,
				"post-1",
				"version-1",
				[]string{"1b6bcad2-c90d-49e9-bec7-f9a4ba6b2894"},
			)
		},
	)
	if err == nil {
		t.Fatal("missing request context was accepted")
	}
	if writer.called {
		t.Fatal("writer was called without authenticated request context")
	}
}

func TestAppendVersionRejectsMissingWriter(t *testing.T) {
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(),
		sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceEditorCollab},
	)
	if err != nil {
		t.Fatal(err)
	}
	err = AppendVersion(
		sharedtelemetry.WithRequestContext(t.Context(), requestContext),
		nil,
		nil,
		sharedtelemetry.AuditPostUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostVersionCreatedAuditRecord(
				metadata,
				"post-1",
				"version-1",
				[]string{"1b6bcad2-c90d-49e9-bec7-f9a4ba6b2894"},
			)
		},
	)
	if err == nil {
		t.Fatal("missing writer was accepted")
	}
}
