package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestEmitSystemUsesSharedTypedRecord(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx := context.Background()
	record, buildErr := sharedtelemetry.NewServiceReadyRecord(
		SystemMetadata(ctx, time.Date(2026, 8, 10, 1, 2, 3, 0, time.FixedZone("KST", 9*60*60))),
		"worker",
	)
	if err := EmitSystem(ctx, record, buildErr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"event":"service.ready"`) ||
		!strings.Contains(output.String(), `"occurred_at":"2026-08-09T16:02:03Z"`) {
		t.Fatalf("system record = %s", output.String())
	}

	want := errors.New("build failed")
	if err := EmitSystem(ctx, sharedtelemetry.SystemRecord{}, want); !errors.Is(err, want) {
		t.Fatalf("build error = %v", err)
	}
}
