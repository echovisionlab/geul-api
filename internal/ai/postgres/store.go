// Package aipostgres is the PostgreSQL metadata AI Store: metadata_ai_job
// and the PGMQ queue of its generate commands, bound to one gorm
// transaction.
package aipostgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// Queue enqueues a durable command with the caller's executor, so the job
// and its command commit together.
type Queue interface {
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}

// NewUnits returns the production unit of work of the metadata use cases
// and job runner.
func NewUnits(db *gorm.DB, queue Queue) (mutation.UnitOfWork[ai.Store], error) {
	if db == nil || queue == nil {
		return nil, errors.New("metadata AI store dependencies are required: DB, Queue")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) ai.Store { return Bind(tx, queue) }), nil
}

// Bind returns the Store over tx.
func Bind(tx *gorm.DB, queue Queue) ai.Store {
	return &store{tx: tx, queue: queue}
}

type store struct {
	tx    *gorm.DB
	queue Queue
}

var _ ai.Store = (*store)(nil)

type jobRecord struct {
	ID                string            `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	RequesterMemberID string            `gorm:"column:requester_member_id;type:uuid;not null"`
	TargetType        string            `gorm:"column:target_type;type:varchar(100);not null"`
	TargetID          string            `gorm:"column:target_id;type:text;not null"`
	RequestedKeys     []string          `gorm:"column:requested_keys;type:jsonb;serializer:json"`
	Context           string            `gorm:"column:context;type:text;not null"`
	Prompt            string            `gorm:"column:prompt;type:text;not null"`
	Status            string            `gorm:"column:status;type:varchar(50);not null"`
	Suggestion        map[string]string `gorm:"column:suggestion;type:jsonb;serializer:json"`
	ResponseText      *string           `gorm:"column:response_text;type:text"`
	Error             *string           `gorm:"column:error;type:text"`
	Provider          *string           `gorm:"column:provider;type:varchar(100)"`
	Model             *string           `gorm:"column:model;type:varchar(255)"`
	DurationMS        *int64            `gorm:"column:duration_ms"`
	StartedAt         *time.Time        `gorm:"column:started_at"`
	CompletedAt       *time.Time        `gorm:"column:completed_at"`
	ResolvedAt        *time.Time        `gorm:"column:resolved_at"`
	CreatedAt         time.Time         `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time         `gorm:"column:updated_at;not null;default:now()"`
}

func (jobRecord) TableName() string {
	return "metadata_ai_job"
}

func (s *store) InsertJob(ctx context.Context, job ai.Job) (ai.Job, error) {
	row := recordOf(job)
	if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return ai.Job{}, err
	}
	return jobOf(row), nil
}

func (s *store) Job(ctx context.Context, jobID string) (ai.Job, error) {
	var row jobRecord
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ai.Job{}, notFoundError{err}
		}
		return ai.Job{}, err
	}
	return jobOf(row), nil
}

// notFoundError keeps gorm's not-found error readable and in the chain
// while it answers ai.ErrJobNotFound.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == ai.ErrJobNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func (s *store) ResolveJob(ctx context.Context, jobID string, status ai.JobStatus, now time.Time) (bool, error) {
	result := s.tx.WithContext(ctx).Model(&jobRecord{}).
		Where("id = ? AND status = ?", jobID, string(ai.JobReady)).
		Updates(structured.Fields{"status": string(status), "resolved_at": now, "updated_at": now})
	return result.RowsAffected != 0, result.Error
}

func (s *store) ClaimJob(ctx context.Context, job ai.Job, provider, modelName string, startedAt time.Time) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&jobRecord{})
	switch job.Status {
	case ai.JobQueued:
		query = query.Where("id = ? AND status = ?", job.ID, string(ai.JobQueued))
	case ai.JobRunning:
		query = query.Where("id = ? AND status = ?", job.ID, string(ai.JobRunning))
		if job.StartedAt == nil {
			query = query.Where("started_at IS NULL")
		} else {
			query = query.Where("started_at = ?", *job.StartedAt)
		}
	default:
		return false, nil
	}
	result := query.Updates(structured.Fields{
		"status":     string(ai.JobRunning),
		"provider":   provider,
		"model":      modelName,
		"started_at": startedAt,
		"updated_at": startedAt,
	})
	return result.RowsAffected != 0, result.Error
}

func (s *store) FinishJob(ctx context.Context, lease ai.Lease, outcome ai.JobOutcome) (bool, error) {
	updates := structured.Fields{
		"status":       string(outcome.Status),
		"error":        outcome.Error,
		"duration_ms":  outcome.DurationMS,
		"completed_at": outcome.CompletedAt,
		"updated_at":   outcome.CompletedAt,
	}
	if outcome.Status == ai.JobReady {
		suggestionJSON, err := json.Marshal(outcome.Suggestion)
		if err != nil {
			return false, err
		}
		updates["suggestion"] = string(suggestionJSON)
		updates["response_text"] = outcome.ResponseText
		updates["error"] = nil
	} else if outcome.Error != nil {
		updates["error"] = *outcome.Error
	}
	result := s.tx.WithContext(ctx).
		Model(&jobRecord{}).
		Where("id = ? AND status = ? AND started_at = ?", lease.JobID, string(ai.JobRunning), lease.StartedAt).
		Updates(updates)
	return result.RowsAffected != 0, result.Error
}

func (s *store) LockExpiredJobs(ctx context.Context, recoveryBefore, staleBefore time.Time, limit int) ([]ai.Job, error) {
	var rows []jobRecord
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Where(
			"(status = ? AND updated_at <= ?) OR (status = ? AND updated_at <= ? AND (started_at IS NULL OR started_at <= ?))",
			string(ai.JobQueued),
			recoveryBefore,
			string(ai.JobRunning),
			recoveryBefore,
			staleBefore,
		).
		Order("updated_at ASC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	jobs := make([]ai.Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, jobOf(row))
	}
	return jobs, nil
}

func (s *store) TransportPending(ctx context.Context, jobID string) (bool, error) {
	queueTable := pq.QuoteIdentifier("q_" + eventpkg.QueueAiMetadataGenerate)
	var transportPending bool
	query := "SELECT EXISTS (SELECT 1 FROM pgmq." + queueTable + " WHERE message->>'message_id' = ?)"
	err := s.tx.WithContext(ctx).Raw(query, jobID).Scan(&transportPending).Error
	return transportPending, err
}

func (s *store) TouchExpiredJob(ctx context.Context, job ai.Job, now time.Time) (bool, error) {
	update := s.tx.WithContext(ctx).Model(&jobRecord{}).
		Where("id = ? AND status = ? AND updated_at = ?", job.ID, string(job.Status), job.UpdatedAt)
	if job.Status == ai.JobRunning {
		if job.StartedAt == nil {
			update = update.Where("started_at IS NULL")
		} else {
			update = update.Where("started_at = ?", *job.StartedAt)
		}
	}
	result := update.Updates(structured.Fields{"updated_at": now})
	return result.RowsAffected != 0, result.Error
}

func (s *store) EnqueueGenerate(ctx context.Context, jobID string) error {
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	return s.queue.EnqueueProtobufWithExecutor(
		ctx, executor, eventpkg.QueueAiMetadataGenerate, jobID,
		&managev1.MetadataGenerationQueueEvent{JobId: jobID},
	)
}

func recordOf(job ai.Job) jobRecord {
	return jobRecord{
		ID: job.ID, RequesterMemberID: job.RequesterMemberID, TargetType: job.TargetType, TargetID: job.TargetID,
		RequestedKeys: job.RequestedKeys, Context: job.Context, Prompt: job.Prompt, Status: string(job.Status),
		Suggestion: job.Suggestion, ResponseText: job.ResponseText, Error: job.Error, Provider: job.Provider,
		Model: job.Model, DurationMS: job.DurationMS, StartedAt: job.StartedAt, CompletedAt: job.CompletedAt,
		ResolvedAt: job.ResolvedAt, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
}

func jobOf(row jobRecord) ai.Job {
	return ai.Job{
		ID: row.ID, RequesterMemberID: row.RequesterMemberID, TargetType: row.TargetType, TargetID: row.TargetID,
		RequestedKeys: row.RequestedKeys, Context: row.Context, Prompt: row.Prompt, Status: ai.JobStatus(row.Status),
		Suggestion: row.Suggestion, ResponseText: row.ResponseText, Error: row.Error, Provider: row.Provider,
		Model: row.Model, DurationMS: row.DurationMS, StartedAt: row.StartedAt, CompletedAt: row.CompletedAt,
		ResolvedAt: row.ResolvedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
