package mq

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

type TranscoderPublisher interface {
	PublishTranscodeAudio(context.Context, *managev1.TranscodeAudioEvent) error
	PublishTranscodeVideo(context.Context, *managev1.TranscodeVideoEvent) error
	PublishWaveformCancel(context.Context, *managev1.WaveformCancelEvent) error
}

type MeshOptimizationPublisher interface {
	PublishMeshOptimizationJob(context.Context, *managev1.MeshOptimizationJob) error
}

type EmailPublisher interface {
	TransactionalAsyncPublisher
	PublishSendEmail(context.Context, *managev1.SendEmailEvent) error
	PublishSendBulkEmail(context.Context, *managev1.SendBulkEmailBatchEvent) error
}

type UserDeletionPublisher interface {
	PublishUserDeleteIdentity(context.Context, *managev1.UserDeleteIdentityCommand) error
	PublishUserDeleteAvatar(context.Context, *managev1.UserDeleteAvatarCommand) error
}

type TranslationPublisher interface {
	PublishTranslationGenerate(context.Context, *managev1.TranslationGenerateEvent) error
	PublishTranslationLifecycle(context.Context, *managev1.TranslationLifecycleEvent) error
	PublishContentUpdated(context.Context, *managev1.ContentUpdatedEvent) error
	PublishContentUpdatedWithExecutor(context.Context, eventpkg.DBTX, *managev1.ContentUpdatedEvent) error
}

// AsyncPublisher is the transport-neutral capability exposed to domain
// services. Durable work is enqueued in PGMQ; ephemeral wake-ups are PostgreSQL
// NOTIFY signals.
type AsyncPublisher interface {
	EnqueueProtobuf(context.Context, string, string, proto.Message) error
	NotifyProtobuf(context.Context, string, proto.Message) error
}

// TransactionalAsyncPublisher is required whenever a durable command is
// written beside product state. The implementation must enqueue through the
// supplied executor, which is normally the caller's *sql.Tx.
type TransactionalAsyncPublisher interface {
	AsyncPublisher
	EnqueueProtobufWithExecutor(context.Context, eventpkg.DBTX, string, string, proto.Message) error
}

type Publisher struct {
	db *sql.DB
}

func NewPublisher(db *sql.DB) (*Publisher, error) {
	if db == nil {
		return nil, fmt.Errorf("PostgreSQL connection is required")
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("connect PGMQ publisher: %w", err)
	}
	return &Publisher{db: db}, nil
}

func (p *Publisher) EnqueueProtobuf(
	ctx context.Context,
	queue string,
	messageID string,
	message proto.Message,
) error {
	return EnqueueProtobuf(ctx, p.db, queue, messageID, message)
}

func (p *Publisher) EnqueueProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	return EnqueueProtobuf(ctx, executor, queue, messageID, message)
}

// EnqueueProtobuf accepts *sql.DB, *sql.Conn, or the caller's *sql.Tx. Passing
// the owning transaction commits domain state and its command atomically.
func EnqueueProtobuf(
	ctx context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return fmt.Errorf("PGMQ executor is required")
	}
	queue = strings.TrimSpace(queue)
	messageID = strings.TrimSpace(messageID)
	if queue == "" {
		return fmt.Errorf("queue is required")
	}
	if messageID == "" {
		return fmt.Errorf("message id is required")
	}
	if message == nil {
		return fmt.Errorf("protobuf message is required")
	}
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal protobuf: %w", err)
	}
	envelope, err := eventpkg.NewEnvelope(messageID, string(message.ProtoReflect().Descriptor().FullName()), body)
	if err != nil {
		return err
	}
	startedAt := time.Now()
	_, err = (eventpkg.PGMQ{}).Enqueue(ctx, executor, queue, envelope, InjectTraceContext(ctx), 0)
	if err != nil {
		emitQueuePublishResult(ctx, queue, messageID, time.Since(startedAt), sharedtelemetry.QueueFailureEnqueueFailed)
		return err
	}
	emitQueuePublishResult(ctx, queue, messageID, time.Since(startedAt), "")
	return nil
}

func (p *Publisher) NotifyProtobuf(ctx context.Context, signal string, message proto.Message) error {
	if message != nil &&
		!(signal == eventpkg.SignalContentUpdated && aidocument.InteractivePostCommitCompletionOwnsSignal(ctx)) &&
		(p == nil || p.db == nil) {
		return fmt.Errorf("PostgreSQL publisher is required")
	}
	var executor eventpkg.DBTX
	if p != nil && p.db != nil {
		executor = p.db
	}
	return p.notifyProtobufWithExecutor(ctx, executor, signal, message)
}

// NotifyProtobufWithExecutor schedules one PostgreSQL signal in the caller's
// transaction. PostgreSQL releases the notification only when that transaction
// commits and discards it on rollback.
func (p *Publisher) NotifyProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	return p.notifyProtobufWithExecutor(ctx, executor, signal, message)
}

func (p *Publisher) notifyProtobufWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	if p == nil {
		return fmt.Errorf("PostgreSQL publisher is required")
	}
	if message == nil {
		return fmt.Errorf("protobuf message is required")
	}
	if signal == eventpkg.SignalContentUpdated && aidocument.InteractivePostCommitCompletionOwnsSignal(ctx) {
		return nil
	}
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal protobuf: %w", err)
	}
	return notifyWithExecutor(
		ctx,
		executor,
		signal,
		uuid.NewString(),
		string(message.ProtoReflect().Descriptor().FullName()),
		body,
	)
}

func (p *Publisher) notify(ctx context.Context, signal, messageID, messageType string, body []byte) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("PostgreSQL publisher is required")
	}
	return notifyWithExecutor(ctx, p.db, signal, messageID, messageType, body)
}

func notifyWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	signal string,
	messageID string,
	messageType string,
	body []byte,
) error {
	if executor == nil {
		return fmt.Errorf("PostgreSQL signal executor is required")
	}
	signal = strings.TrimSpace(signal)
	if signal == "" {
		return fmt.Errorf("signal is required")
	}
	envelope, err := eventpkg.NewEnvelope(messageID, messageType, body)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal signal envelope: %w", err)
	}
	if len(payload) > 7900 {
		return fmt.Errorf("signal %s payload exceeds PostgreSQL NOTIFY limit", signal)
	}
	if _, err := executor.ExecContext(ctx, "SELECT pg_notify($1, $2)", signal, string(payload)); err != nil {
		return fmt.Errorf("notify %s: %w", signal, err)
	}
	return nil
}

func (p *Publisher) PublishOgLifecycle(ctx context.Context, payload []byte) error {
	if len(payload) == 0 {
		return fmt.Errorf("OG lifecycle payload is required")
	}
	return p.notify(ctx, eventpkg.SignalOgLifecycle, uuid.NewString(), "geul.og.lifecycle", append([]byte(nil), payload...))
}

func (p *Publisher) PublishTranscodeAudio(ctx context.Context, job *managev1.TranscodeAudioEvent) error {
	if job == nil {
		return fmt.Errorf("transcode audio job is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueTranscoderAudio, job.GetEventId(), job)
}

func (p *Publisher) PublishTranscodeVideo(ctx context.Context, job *managev1.TranscodeVideoEvent) error {
	if job == nil {
		return fmt.Errorf("transcode video job is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueTranscoderVideo, job.GetEventId(), job)
}

func (p *Publisher) PublishWaveformGenerate(ctx context.Context, job *managev1.WaveformGenerateEvent) error {
	if job == nil {
		return fmt.Errorf("waveform job is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueWaveformGenerate, job.GetEventId(), job)
}

func (p *Publisher) PublishMeshOptimizationJob(ctx context.Context, job *managev1.MeshOptimizationJob) error {
	if job == nil {
		return fmt.Errorf("mesh optimization job is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueAssetOptimizerMesh, job.GetJobId(), job)
}

func (p *Publisher) PublishFileDelete(ctx context.Context, job *managev1.FileDeleteEvent) error {
	if job == nil {
		return fmt.Errorf("file delete job is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueFileDelete, job.GetFileId(), job)
}

func (p *Publisher) PublishFileDeleteWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	job *managev1.FileDeleteEvent,
) error {
	if job == nil {
		return fmt.Errorf("file delete job is required")
	}
	return EnqueueProtobuf(ctx, executor, eventpkg.QueueFileDelete, job.GetFileId(), job)
}

func (p *Publisher) PublishUserDeleteIdentity(ctx context.Context, command *managev1.UserDeleteIdentityCommand) error {
	if err := validateUserDeleteIdentityMode(command); err != nil {
		return err
	}
	memberID, err := userDeletionCommandMemberID(command)
	if err != nil {
		return err
	}
	identityID, err := canonicalUserDeletionID(command.GetIdentityId(), "identity_id")
	if err != nil {
		return err
	}
	if memberID == identityID {
		return fmt.Errorf("member_id and identity_id must be distinct")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueUserDeleteIdentity, "user-delete-identity:"+memberID, command)
}

func (p *Publisher) PublishUserDeleteIdentityWithExecutor(ctx context.Context, executor eventpkg.DBTX, command *managev1.UserDeleteIdentityCommand) error {
	if err := validateUserDeleteIdentityMode(command); err != nil {
		return err
	}
	memberID, err := userDeletionCommandMemberID(command)
	if err != nil {
		return err
	}
	identityID, err := canonicalUserDeletionID(command.GetIdentityId(), "identity_id")
	if err != nil {
		return err
	}
	if memberID == identityID {
		return fmt.Errorf("member_id and identity_id must be distinct")
	}
	return EnqueueProtobuf(ctx, executor, eventpkg.QueueUserDeleteIdentity, "user-delete-identity:"+memberID, command)
}

func validateUserDeleteIdentityMode(command *managev1.UserDeleteIdentityCommand) error {
	if command == nil {
		return fmt.Errorf("user deletion identity command is required")
	}
	switch command.GetMode() {
	case managev1.UserDeleteIdentityMode_TOMBSTONE,
		managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE:
		return nil
	default:
		return fmt.Errorf("user deletion identity mode must be explicit")
	}
}

func (p *Publisher) PublishUserDeleteAvatar(ctx context.Context, command *managev1.UserDeleteAvatarCommand) error {
	memberID, err := userDeletionCommandMemberID(command)
	if err != nil {
		return err
	}
	if command.GetAvatarAssetId() != "" {
		if _, err := canonicalUserDeletionID(command.GetAvatarAssetId(), "avatar_asset_id"); err != nil {
			return err
		}
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueUserDeleteAvatar, "user-delete-avatar:"+memberID, command)
}

func (p *Publisher) PublishUserDeleteAvatarWithExecutor(ctx context.Context, executor eventpkg.DBTX, command *managev1.UserDeleteAvatarCommand) error {
	memberID, err := userDeletionCommandMemberID(command)
	if err != nil {
		return err
	}
	if command.GetAvatarAssetId() != "" {
		if _, err := canonicalUserDeletionID(command.GetAvatarAssetId(), "avatar_asset_id"); err != nil {
			return err
		}
	}
	return EnqueueProtobuf(ctx, executor, eventpkg.QueueUserDeleteAvatar, "user-delete-avatar:"+memberID, command)
}

type userDeletionCommand interface {
	proto.Message
	GetMemberId() string
}

func userDeletionCommandMemberID(command userDeletionCommand) (string, error) {
	if command == nil {
		return "", fmt.Errorf("user deletion command is required")
	}
	return canonicalUserDeletionID(command.GetMemberId(), "member_id")
}

func canonicalUserDeletionID(value, field string) (string, error) {
	parsed, err := uuidutil.ParseCanonical(value, field)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func (p *Publisher) PublishSendEmail(ctx context.Context, job *managev1.SendEmailEvent) error {
	return p.publishEmail(ctx, eventpkg.QueueEmailSend, job)
}

func (p *Publisher) PublishAuthEmail(ctx context.Context, job *managev1.SendEmailEvent) error {
	return p.publishEmail(ctx, eventpkg.QueueEmailAuth, job)
}

func (p *Publisher) publishEmail(ctx context.Context, queue string, job *managev1.SendEmailEvent) error {
	if job == nil {
		return fmt.Errorf("email job is required")
	}
	if strings.TrimSpace(job.GetMessageId()) == "" {
		return fmt.Errorf("email message id is required")
	}
	return p.EnqueueProtobuf(ctx, queue, job.GetMessageId(), job)
}

func (p *Publisher) PublishSendBulkEmail(ctx context.Context, job *managev1.SendBulkEmailBatchEvent) error {
	if job == nil {
		return fmt.Errorf("bulk email batch is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueEmailCampaign, job.GetDeliveryRunId(), job)
}

func (p *Publisher) PublishTranslationGenerate(ctx context.Context, job *managev1.TranslationGenerateEvent) error {
	if job == nil {
		return fmt.Errorf("translation generation job is required")
	}
	return p.EnqueueProtobuf(ctx, eventpkg.QueueTranslationGenerate, job.GetJobId(), job)
}

func (p *Publisher) PublishTranscodeProgress(ctx context.Context, message *managev1.TranscodeProgressEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalTranscodeProgress, message)
}

func (p *Publisher) PublishTranscodeCancel(ctx context.Context, message *managev1.TranscodeCancelEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalTranscodeCancel, message)
}

func (p *Publisher) PublishWaveformCancel(ctx context.Context, message *managev1.WaveformCancelEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalWaveformCancel, message)
}

func (p *Publisher) PublishMediaProcessingLifecycle(ctx context.Context, message *managev1.MediaProcessingLifecycleEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalMediaProcessingLifecycle, message)
}

func (p *Publisher) PublishFileIngest(ctx context.Context, message proto.Message) error {
	if err := ValidateFileIngestSignal(message); err != nil {
		return err
	}
	if attached, ok := message.(*managev1.FileIngestAttachedEvent); ok {
		messageID, err := FileIngestAttachedMessageID(attached)
		if err != nil {
			return err
		}
		if err := p.EnqueueProtobuf(ctx, eventpkg.QueueReleaseTrackOriginalAudioProjection, messageID, attached); err != nil {
			return err
		}
	}
	return p.NotifyProtobuf(ctx, eventpkg.SignalFileIngest, message)
}

func FileIngestAttachedMessageID(message *managev1.FileIngestAttachedEvent) (string, error) {
	if message == nil || message.GetIdentity() == nil {
		return "", fmt.Errorf("file ingest attached identity is required")
	}
	identity := message.GetIdentity()
	if identity.GetEntityType() != managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK ||
		identity.GetMediaKind() != managev1.FileIngestMediaKind_FILE_INGEST_MEDIA_KIND_TRACK_AUDIO {
		return "", fmt.Errorf("file ingest attached is reserved for Track original audio")
	}
	fileID := strings.TrimSpace(identity.GetFileId())
	if fileID == "" {
		return "", fmt.Errorf("file ingest attached file id is required")
	}
	return "file-ingest-attached:" + fileID, nil
}

func ValidateFileIngestSignal(message proto.Message) error {
	switch event := message.(type) {
	case *managev1.FileIngestUploadEvent:
		return nil
	case *managev1.FileIngestDownloadEvent:
		return nil
	case *managev1.FileIngestFinalizedEvent:
		return nil
	case *managev1.FileIngestAttachedEvent:
		_, err := FileIngestAttachedMessageID(event)
		return err
	case *managev1.FileIngestFailedEvent:
		return nil
	default:
		return fmt.Errorf("unsupported file ingest event type %T", message)
	}
}

func (p *Publisher) PublishMailAdapterChanged(ctx context.Context, message *managev1.MailAdapterChangedEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalMailAdapterChanged, message)
}

func (p *Publisher) PublishTranslationLifecycle(ctx context.Context, message *managev1.TranslationLifecycleEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalTranslationLifecycle, message)
}

func (p *Publisher) PublishContentUpdated(ctx context.Context, message *managev1.ContentUpdatedEvent) error {
	return p.NotifyProtobuf(ctx, eventpkg.SignalContentUpdated, message)
}

func (p *Publisher) PublishContentUpdatedWithExecutor(
	ctx context.Context,
	executor eventpkg.DBTX,
	message *managev1.ContentUpdatedEvent,
) error {
	return p.NotifyProtobufWithExecutor(ctx, executor, eventpkg.SignalContentUpdated, message)
}

func (p *Publisher) Close() error { return nil }
