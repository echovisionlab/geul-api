package mcp

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const responseTestTranslationJobID = "11111111-1111-4111-8111-111111111111"

func TestTranslationJobEncodersShareCanonicalCompactConversion(t *testing.T) {
	jobs := []*managev1.TranslationJob{responseTestTranslationJob(responseTestTranslationJobID, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING)}
	regenerated, err := encodeTranslationJobs(jobs)
	if err != nil {
		t.Fatalf("encodeTranslationJobs() error = %v", err)
	}
	listed, err := encodeTranslationJobList(jobs, &commonv1.PaginationResponse{})
	if err != nil {
		t.Fatalf("encodeTranslationJobList() error = %v", err)
	}
	for _, encoded := range [][]byte{regenerated, listed} {
		if !strings.Contains(string(encoded), `"i":"`+responseTestTranslationJobID+`"`) ||
			!strings.Contains(string(encoded), `"s":"running"`) {
			t.Fatalf("encoded Job = %s", encoded)
		}
		for _, removed := range []string{`"c":`, `"co":`, `"f":`} {
			if strings.Contains(string(encoded), removed) {
				t.Fatalf("encoded Job retained removed field %s: %s", removed, encoded)
			}
		}
	}
}

func TestTranslationJobEncodersUseEmptyArraysForEmptySnapshots(t *testing.T) {
	encoded, err := encodeTranslationJobs(nil)
	if err != nil {
		t.Fatalf("encodeTranslationJobs(nil) error = %v", err)
	}
	if string(encoded) != `{"j":[]}` {
		t.Fatalf("encodeTranslationJobs(nil) = %s", encoded)
	}
}

func TestCompactTranslationJobSchemaMatchesEncodedShape(t *testing.T) {
	job := responseTestTranslationJob(responseTestTranslationJobID, managev1.TranslationJobStatus_TRANSLATION_JOB_STATUS_RUNNING)
	job.StartedAt = timestamppb.New(job.RequestedAt.AsTime().Add(time.Minute))
	compact, err := compactTranslationJob(job)
	if err != nil {
		t.Fatalf("compactTranslationJob() error = %v", err)
	}
	encoded, err := json.Marshal(compact)
	if err != nil {
		t.Fatalf("json.Marshal(compactJob) error = %v", err)
	}

	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("json.Unmarshal(compactJob) error = %v", err)
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(compactJobDefinitionJSONSchema), &schema); err != nil {
		t.Fatalf("compact Job schema is invalid: %v", err)
	}

	wantProperties := []string{"d", "i", "o", "p", "rq", "s", "sl", "st", "tl"}
	gotProperties := make([]string, 0, len(schema.Properties))
	for property := range schema.Properties {
		gotProperties = append(gotProperties, property)
	}
	slices.Sort(gotProperties)
	if !reflect.DeepEqual(gotProperties, wantProperties) {
		t.Fatalf("compact Job schema properties = %v, want %v", gotProperties, wantProperties)
	}
	wantRequired := []string{"d", "i", "o", "p", "s", "sl", "tl"}
	slices.Sort(schema.Required)
	if !reflect.DeepEqual(schema.Required, wantRequired) {
		t.Fatalf("compact Job schema required = %v, want %v", schema.Required, wantRequired)
	}
	for property := range output {
		if _, ok := schema.Properties[property]; !ok {
			t.Fatalf("encoded compact Job property %q is absent from schema: %s", property, encoded)
		}
	}
	for _, required := range schema.Required {
		if _, ok := output[required]; !ok {
			t.Fatalf("compact Job schema requires missing property %q: %s", required, encoded)
		}
	}
	if status := schema.Properties["s"]["enum"]; !reflect.DeepEqual(status, []any{"queued", "running"}) {
		t.Fatalf("compact Job status schema = %#v", status)
	}
}

func TestTranslationJobCancellationSchemaMatchesAcknowledgement(t *testing.T) {
	encoded, err := encodeTranslationJobCancellation(responseTestTranslationJobID)
	if err != nil {
		t.Fatalf("encodeTranslationJobCancellation() error = %v", err)
	}
	if string(encoded) != `{"j":"`+responseTestTranslationJobID+`"}` {
		t.Fatalf("translation Job cancellation = %s", encoded)
	}

	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(translationJobCancellationOutputJSONSchema), &schema); err != nil {
		t.Fatalf("translation Job cancellation schema is invalid: %v", err)
	}
	if !reflect.DeepEqual(schema.Required, []string{"j"}) || len(schema.Properties) != 1 || schema.Properties["j"]["format"] != "uuid" {
		t.Fatalf("translation Job cancellation schema = %+v", schema)
	}
}

func responseTestTranslationJob(id string, status managev1.TranslationJobStatus) *managev1.TranslationJob {
	return &managev1.TranslationJob{
		Id: id,
		Target: &managev1.TranslationTarget{
			EntityType: managev1.TranslationEntityType_TRANSLATION_ENTITY_TYPE_POST,
			EntityId:   "post-a",
		},
		TargetLocale: "en",
		SourceLocale: "ko",
		Status:       status,
		OperationId:  "55555555-5555-4555-8555-555555555555",
		RequestedAt:  timestamppb.New(time.Date(2026, 8, 23, 1, 2, 3, 0, time.UTC)),
	}
}
