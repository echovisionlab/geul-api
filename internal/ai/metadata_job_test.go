package ai

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/llm"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/proto"
)

func TestParseMetadataSuggestionPayload(t *testing.T) {
	t.Parallel()

	t.Run("recovers wrapped JSON and snake_case keys", func(t *testing.T) {
		t.Parallel()

		suggestion, err := parseMetadataSuggestionPayload(
			`Here is the result: {"metadata":{"summary":"Quiet summary","notes":"ignore me"}}`,
			[]string{"summary"},
		)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"summary": "Quiet summary",
		}, suggestion)
	})

	t.Run("rejects payloads without requested fields", func(t *testing.T) {
		t.Parallel()

		suggestion, err := parseMetadataSuggestionPayload(
			`{"notes":"Measured title"}`,
			[]string{"summary"},
		)
		require.Error(t, err)
		assert.Nil(t, suggestion)
	})
}

func TestValidateMetadataAIUserPromptRequiresSupportedRequestedKeys(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateMetadataAIUserPrompt(`{"task":{"requestedKeys":["summary"]},"source":{"title":"Page"}}`))
	require.Error(t, validateMetadataAIUserPrompt(`{"task":{"requestedKeys":[]},"source":{"title":"Page"}}`))
	require.Error(t, validateMetadataAIUserPrompt(`{"task":{"requestedKeys":["title"]},"source":{"title":"Page"}}`))
}

func TestMetadataSuggestionRegistryKeepsSchemaValidationAndProtoMappingInParity(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, metadataSuggestionRegistry)
	descriptor := (&managev1.MetadataSuggestion{}).ProtoReflect().Descriptor()
	for key, definition := range metadataSuggestionRegistry {
		t.Run(key, func(t *testing.T) {
			field := descriptor.Fields().ByJSONName(key)
			require.NotNil(t, field, "accepted metadata keys must map to a protobuf field")
			require.Equal(t, field.Kind().String(), definition.responseSchema["type"])
			require.NotNil(t, definition.setProto)

			prompt := fmt.Sprintf(`{"task":{"requestedKeys":[%q]}}`, key)
			require.NoError(t, validateMetadataAIUserPrompt(prompt))
			schema := buildMetadataResponseJSONSchema(prompt)
			properties, ok := schema["properties"].(structured.Fields)
			require.True(t, ok)
			require.Contains(t, properties, key)
			require.Equal(t, []string{key}, schema["required"])

			parsed, err := parseMetadataSuggestionPayload(fmt.Sprintf(`{"%s":" mapped value "}`, key), []string{key})
			require.NoError(t, err)
			require.Equal(t, "mapped value", parsed[key])

			message := buildMetadataSuggestionMessage(parsed)
			reflected := message.ProtoReflect()
			require.True(t, reflected.Has(field), "accepted metadata keys must map to a present protobuf field")
			require.Equal(t, parsed[key], reflected.Get(field).String())
		})
	}
}

func TestProcessMetadataAIJobReclaimsStaleRunningJobAfterRestart(t *testing.T) {
	db := newMetadataJobUnitDB(t)
	provider := &metadataJobTestProvider{text: `{"metadata":{"summary":"Recovered"}}`}
	manager := &MetadataJobManager{db: db, provider: provider}
	job := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusRunning, time.Now().Add(-metadataAIJobLeaseDuration-time.Minute))

	require.NoError(t, manager.ProcessJob(context.Background(), job.ID))
	require.EqualValues(t, 1, provider.calls.Load())
	requireMetadataAIJobUnitStatus(t, db, job.ID, metadataAIJobStatusReady)
}

func TestProcessMetadataAIJobRetriesFreshRunningDeliveryWithoutAcknowledging(t *testing.T) {
	db := newMetadataJobUnitDB(t)
	provider := &metadataJobTestProvider{text: `{"metadata":{"summary":"Must not run twice"}}`}
	manager := &MetadataJobManager{db: db, provider: provider}
	job := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusRunning, time.Now())

	err := manager.ProcessJob(context.Background(), job.ID)
	require.ErrorIs(t, err, errMetadataAIJobLeaseUnavailable)
	require.Zero(t, provider.calls.Load())
	requireMetadataAIJobUnitStatus(t, db, job.ID, metadataAIJobStatusRunning)
}

func TestProcessMetadataAIJobRetriesAfterTransientDatabaseReadFailure(t *testing.T) {
	db := newMetadataJobUnitDB(t)
	provider := &metadataJobTestProvider{text: `{"metadata":{"summary":"Retried"}}`}
	manager := &MetadataJobManager{db: db, provider: provider}
	job := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusQueued, time.Time{})

	databaseErr := errors.New("temporary database outage")
	var failOnce atomic.Bool
	failOnce.Store(true)
	callbackName := "test:metadata_ai_job_transient_read_failure"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if failOnce.CompareAndSwap(true, false) {
			tx.AddError(databaseErr)
		}
	}))
	err := manager.ProcessJob(context.Background(), job.ID)
	require.ErrorIs(t, err, databaseErr)
	require.Zero(t, provider.calls.Load())
	require.NoError(t, db.Callback().Query().Remove(callbackName))

	require.NoError(t, manager.ProcessJob(context.Background(), job.ID))
	require.EqualValues(t, 1, provider.calls.Load())
	requireMetadataAIJobUnitStatus(t, db, job.ID, metadataAIJobStatusReady)
}

func TestProcessMetadataAIJobPersistsCancellationWithBoundedDetachedContext(t *testing.T) {
	db := newMetadataJobUnitDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	provider := &metadataJobTestProvider{
		text:       `{"metadata":{"summary":"Late response"}}`,
		onGenerate: cancel,
	}
	manager := &MetadataJobManager{db: db, provider: provider}
	job := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusQueued, time.Time{})

	require.NoError(t, manager.ProcessJob(ctx, job.ID))
	require.EqualValues(t, 1, provider.calls.Load())
	requireMetadataAIJobUnitStatus(t, db, job.ID, metadataAIJobStatusFailed)
	var stored metadataJobRecord
	require.NoError(t, db.First(&stored, "id = ?", job.ID).Error)
	require.Equal(t, context.Canceled.Error(), *stored.Error)
}

func TestRecoverExpiredMetadataJobsRepublishesOnlyWhenTransportMessageIsMissing(t *testing.T) {
	db := newMetadataJobUnitDB(t)
	publisher := &metadataJobTestPublisher{}
	manager := &MetadataJobManager{db: db, provider: &metadataJobTestProvider{}, asyncPublisher: publisher}
	pending := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusQueued, time.Time{})
	orphan := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusQueued, time.Time{})
	old := time.Now().Add(-metadataAIJobRecoveryDelay - time.Minute).UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Model(&metadataJobRecord{}).Where("id IN ?", []string{pending.ID, orphan.ID}).Update("updated_at", old).Error)
	require.NoError(t, db.Exec(`INSERT INTO pgmq."q_ai.metadata.generate" (message) VALUES (?)`, fmt.Sprintf(`{"message_id":%q}`, pending.ID)).Error)

	recovered, err := manager.RecoverExpiredJobs(context.Background(), 25)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, 1, publisher.calls)

	recovered, err = manager.RecoverExpiredJobs(context.Background(), 25)
	require.NoError(t, err)
	require.Zero(t, recovered, "the updated_at cooldown must prevent repeat enqueue")
	require.Equal(t, 1, publisher.calls)
	var stored metadataJobRecord
	require.NoError(t, db.First(&stored, "id = ?", orphan.ID).Error)
	require.True(t, stored.UpdatedAt.After(old))
}

func TestRecoverExpiredMetadataJobsRepublishesStaleRunningLease(t *testing.T) {
	db := newMetadataJobUnitDB(t)
	publisher := &metadataJobTestPublisher{}
	provider := &metadataJobTestProvider{text: `{"metadata":{"summary":"Recovered after scheduler wakeup"}}`}
	manager := &MetadataJobManager{db: db, provider: provider, asyncPublisher: publisher}
	startedAt := time.Now().Add(-metadataAIJobRecoveryDelay - time.Minute).UTC().Truncate(time.Microsecond)
	job := seedMetadataJobUnitRecord(t, db, metadataAIJobStatusRunning, startedAt)
	oldUpdatedAt := time.Now().Add(-metadataAIJobRecoveryDelay - time.Minute).UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Model(&metadataJobRecord{}).Where("id = ?", job.ID).Update("updated_at", oldUpdatedAt).Error)

	recovered, err := manager.RecoverExpiredJobs(context.Background(), 25)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, 1, publisher.calls)

	require.NoError(t, manager.ProcessJob(context.Background(), job.ID))
	require.EqualValues(t, 1, provider.calls.Load())
	requireMetadataAIJobUnitStatus(t, db, job.ID, metadataAIJobStatusReady)
}

func newMetadataJobUnitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	_, err = sqlDB.Exec("ATTACH DATABASE ':memory:' AS pgmq")
	require.NoError(t, err)
	_, err = sqlDB.Exec(`CREATE TABLE pgmq."q_ai.metadata.generate" (message TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE metadata_ai_job (
		id TEXT PRIMARY KEY,
		requester_member_id TEXT,
		target_type TEXT,
		target_id TEXT,
		requested_keys TEXT,
		context TEXT,
		prompt TEXT,
		status TEXT,
		suggestion TEXT,
		response_text TEXT,
		error TEXT,
		provider TEXT,
		model TEXT,
		duration_ms INTEGER,
		started_at DATETIME,
		completed_at DATETIME,
		resolved_at DATETIME,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error)
	return db
}

func seedMetadataJobUnitRecord(t *testing.T, db *gorm.DB, status string, startedAt time.Time) *metadataJobRecord {
	t.Helper()
	job := &metadataJobRecord{
		ID:            uuid.NewString(),
		TargetType:    "AI_RESOURCE_TYPE_PAGE",
		TargetID:      uuid.NewString(),
		RequestedKeys: []string{"summary"},
		Context:       `{"task":{"requestedKeys":["summary"]},"source":{"title":"Unit fixture"}}`,
		Prompt:        "Generate a short summary",
		Status:        status,
	}
	if !startedAt.IsZero() {
		job.StartedAt = &startedAt
	}
	require.NoError(t, db.Create(job).Error)
	return job
}

func requireMetadataAIJobUnitStatus(t *testing.T, db *gorm.DB, jobID string, status string) {
	t.Helper()
	var job metadataJobRecord
	require.NoError(t, db.First(&job, "id = ?", jobID).Error)
	require.Equal(t, status, job.Status)
}

type metadataJobTestProvider struct {
	text       string
	onGenerate func()
	calls      atomic.Int32
}

func (p *metadataJobTestProvider) GenerateText(context.Context, llm.GenerationRequest) (string, error) {
	p.calls.Add(1)
	if p.onGenerate != nil {
		p.onGenerate()
	}
	return p.text, nil
}

func (*metadataJobTestProvider) StartSession(context.Context, llm.SessionSpec) (llm.Session, error) {
	return nil, errors.New("sessions are not supported by the metadata job test provider")
}

func (*metadataJobTestProvider) ProviderName() string { return "metadata-job-test" }

func (*metadataJobTestProvider) ModelName() string { return "metadata-job-test-model" }

type metadataJobTestPublisher struct {
	calls int
}

func (p *metadataJobTestPublisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	_ string,
	messageID string,
	message proto.Message,
) error {
	if _, ok := message.(*managev1.MetadataGenerationQueueEvent); !ok {
		return fmt.Errorf("unexpected queue message type %T", message)
	}
	_, err := executor.ExecContext(ctx,
		`INSERT INTO pgmq."q_ai.metadata.generate" (message) VALUES (?)`,
		fmt.Sprintf(`{"message_id":%q}`, messageID),
	)
	if err == nil {
		p.calls++
	}
	return err
}
