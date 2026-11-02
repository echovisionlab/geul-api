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
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	EmitSystem(ctx, record, buildErr)
	if !strings.Contains(output.String(), `"event":"service.ready"`) ||
		!strings.Contains(output.String(), `"occurred_at":"2026-08-09T16:02:03Z"`) {
		t.Fatalf("system record = %s", output.String())
	}

	output.Reset()
	EmitSystem(ctx, sharedtelemetry.SystemRecord{}, errors.New("build failed"))
	if !strings.Contains(output.String(), `"msg":"Failed to build media system telemetry"`) ||
		!strings.Contains(output.String(), `"failure_cause":"build failed"`) {
		t.Fatalf("build failure log = %s", output.String())
	}
}
