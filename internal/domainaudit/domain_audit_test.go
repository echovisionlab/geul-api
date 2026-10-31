package domainaudit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type recordingAppender struct {
	record sharedtelemetry.AuditRecord
	called bool
	err    error
}

func (w *recordingAppender) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	w.record = record
	w.called = true
	return w.err
}

func TestAuditFailuresKeepCauseAndAction(t *testing.T) {
	for _, phase := range []string{"build", "append"} {
		t.Run(phase, func(t *testing.T) {
			var output bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
			t.Cleanup(func() { slog.SetDefault(previous) })
			failure := errors.New(`audit store unavailable: column "action" <record> {constraint detail} reader@example.test password=private-password`)
			writer := &recordingAppender{}
			if phase == "append" {
				writer.err = failure
			}
			err := AppendSystem(t.Context(), nil, writer, sharedtelemetry.ServiceEditorCollab, sharedtelemetry.AuditPostUpdated,
				func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
					if phase == "build" {
						return sharedtelemetry.AuditRecord{}, failure
					}
					return sharedtelemetry.NewPostVersionCreatedAuditRecord(metadata, "post-1", "version-1", []string{"1b6bcad2-c90d-49e9-bec7-f9a4ba6b2894"})
				})
			if err != failure { //nolint:errorlint // asserts the error is returned unchanged
				t.Fatalf("error = %v, want original failure", err)
			}
			lines := strings.Split(strings.TrimSpace(output.String()), "\n")
			var entry map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
				t.Fatal(err)
			}
			cause, ok := entry["failure_cause"].(string)
			if !ok || entry["action"] != string(sharedtelemetry.AuditPostUpdated) {
				t.Fatalf("missing cause or action: %v", entry)
			}
			for _, want := range []string{"audit store unavailable", `column "action" <record> {constraint detail}`} {
				if !strings.Contains(cause, want) {
					t.Fatalf("cause missing %q: %s", want, cause)
				}
			}
			for _, private := range []string{"reader@example.test", "private-password"} {
				if strings.Contains(output.String(), private) {
					t.Fatalf("log contains private value %q", private)
				}
			}
		})
	}
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
