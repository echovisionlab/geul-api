package worker

import (
	"context"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/post"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Jobs is the store of one job transaction: the port of each domain a
// scheduled job drives in a transaction the worker opens, bound to it. The
// worker owns no table; workerpostgres binds each port to the transaction.
type Jobs interface {
	// ScheduledPosts is the Post Store the scheduled publishing runs on.
	ScheduledPosts() post.Store
	// MetadataAIRecovery republishes the expired metadata AI jobs.
	MetadataAIRecovery() MetadataAIRecovery
	// LegalActivation activates a scheduled Terms or Privacy version.
	LegalActivation() LegalActivation
}

// MetadataAIRecovery republishes expired metadata AI jobs in the job
// transaction.
type MetadataAIRecovery interface {
	// RecoverExpiredJobs republishes up to limit expired jobs (0: the
	// owner's batch) and returns how many it republished.
	RecoverExpiredJobs(ctx context.Context, limit int) (int, error)
}

// LegalKind is a legal policy the scheduler activates: "terms" or "privacy".
type LegalKind string

// The legal policies whose scheduled versions the worker activates.
const (
	LegalTerms   LegalKind = "terms"
	LegalPrivacy LegalKind = "privacy"
)

// EffectiveNotice is the effective-notice run an activation sealed in its
// transaction, handed back to LegalPolicies after commit. The worker does
// not look inside it.
type EffectiveNotice any

// LegalActivation is the Legal port of one activation transaction.
type LegalActivation interface {
	// ActivateDue activates the scheduled version documentID once it is
	// due, archives the active one and seals the effective notice run of
	// eventKey with templateData. It reports false, without an error, when
	// the version is gone or not due.
	ActivateDue(ctx context.Context, kind LegalKind, documentID, eventKey string, templateData map[string]string, now time.Time) (EffectiveNotice, bool, error)
	// RequestOG switches the canonical OG target of the activated version.
	RequestOG(ctx context.Context, kind LegalKind, documentID, reason string) error
}

// LegalPolicies is the Legal port outside an activation transaction.
type LegalPolicies interface {
	// ScheduledVersions lists the IDs of the kind's scheduled versions,
	// earliest first.
	ScheduledVersions(ctx context.Context, kind LegalKind) ([]string, error)
	// DispatchEffectiveNotice dispatches the notice an activation sealed,
	// best effort, after its transaction committed.
	DispatchEffectiveNotice(ctx context.Context, kind LegalKind, documentID string, now time.Time, notice EffectiveNotice)
}

// FileMedia is the File media runtime: deletions, transcode, waveform and
// mesh optimization results.
type FileMedia interface {
	HandleFileDelete(ctx context.Context, event *managev1.FileDeleteEvent) error
	HandleTranscodeProgress(ctx context.Context, body []byte) error
	HandleTranscodeComplete(ctx context.Context, body []byte) error
	HandleWaveformProgress(ctx context.Context, body []byte) error
	HandleWaveformComplete(ctx context.Context, body []byte) error
	HandleWaveformFail(ctx context.Context, body []byte) error
	HandleMeshOptimizationProgress(ctx context.Context, body []byte) error
	HandleMeshOptimizationComplete(ctx context.Context, body []byte) error
	HandleMeshOptimizationFail(ctx context.Context, body []byte) error
	// ExpireStaleMeshOptimizationCandidates expires the mesh optimization
	// candidates no result reached by now and returns how many.
	ExpireStaleMeshOptimizationCandidates(ctx context.Context, now time.Time) (int64, error)
}

// DanglingFileCleanup deletes the Library files nothing references.
type DanglingFileCleanup interface {
	CleanupDangling(ctx context.Context, now time.Time) error
}

// PublicAssetCleanup deletes the public assets marked for deletion and the
// unready ones older than cutoff.
type PublicAssetCleanup interface {
	DeletePending(ctx context.Context, now time.Time) error
	DeleteExpiredUnready(ctx context.Context, cutoff time.Time) error
}

// OGCleanup marks the OG assets the public asset cleanup deletes.
type OGCleanup interface {
	MarkUnboundReadyAssets(ctx context.Context, cutoff, now time.Time) error
	MarkExpiredTerminalGenerationAssets(ctx context.Context, cutoff, now time.Time) error
}

// FaviconCleanup marks the favicon assets the public asset cleanup deletes.
type FaviconCleanup interface {
	MarkDanglingReadyAssets(ctx context.Context, cutoff, now time.Time) error
}

// ExpiredUpload is an upload session with no activity since the cleanup
// cutoff.
type ExpiredUpload struct {
	UploadID              string
	FileID                string
	UploadType            string
	RequestedMime         string
	Extension             string // of RequestedMime; "bin" when unknown
	EntityID              string
	EntityType            *string
	SlotID                *string
	AttemptID             *string
	ExpectedCurrentFileID *string
	ClientMediaBundleID   *string
	ClientMediaManifest   *string
}

// UploadSessions is the File media port of expired upload sessions.
type UploadSessions interface {
	// ExpiredUploads lists the unfinished sessions with no activity since
	// cutoff.
	ExpiredUploads(ctx context.Context, cutoff time.Time) ([]ExpiredUpload, error)
	// ClaimExpiredUpload aborts the session if it is still unfinished with
	// no activity since cutoff, and reports whether it did.
	ClaimExpiredUpload(ctx context.Context, uploadID string, cutoff time.Time) (bool, error)
	// DeleteAbortedUpload deletes a claimed session with no activity since
	// cutoff and returns how many rows it deleted.
	DeleteAbortedUpload(ctx context.Context, uploadID string, cutoff time.Time) (int64, error)
}

// UploadStorage is the object storage of uploads.
type UploadStorage interface {
	// AbortMultipartUpload aborts uploadID of objectKey. Its error is the
	// object storage's, which filemedia.IsMissingMultipartUploadAbortError
	// recognizes when the upload is already gone.
	AbortMultipartUpload(ctx context.Context, objectKey, uploadID string) error
	// CleanupClientMediaStaging deletes a client media bundle's staged
	// objects.
	CleanupClientMediaStaging(ctx context.Context, bundleID, manifest string) error
}

// FileIngestPublisher publishes the realtime file ingest lifecycle.
type FileIngestPublisher interface {
	PublishFileIngest(ctx context.Context, event proto.Message) error
}

// ShareLinks deletes the expired share links.
type ShareLinks interface {
	DeleteExpiredLinks(ctx context.Context, now time.Time) (int64, error)
}

// AuthCodeIssuances deletes expired authentication code issuance rows.
type AuthCodeIssuances interface {
	CleanupExpired(ctx context.Context, now time.Time, limit int) (int64, error)
}

// QueueArchivePurge is how many expired archive rows one queue lost.
type QueueArchivePurge struct {
	Queue   string
	Deleted int64
}

// QueueArchives purges the expired PGMQ archive rows.
type QueueArchives interface {
	PurgeExpired(ctx context.Context) ([]QueueArchivePurge, error)
}

// ScheduledDeletion is a Member deletion request past its grace period.
type ScheduledDeletion struct {
	ID         string
	MemberID   string
	IdentityID string
}

// MemberDeletions is the Account port of Member deletion: the scheduled
// dispatch and the queued commands. Account owns each step's rules.
type MemberDeletions interface {
	// ScrubExpiredEmailProjections scrubs the Member email projections
	// whose retention ended and returns how many.
	ScrubExpiredEmailProjections(ctx context.Context, now time.Time) (int64, error)
	// EnqueueExpiredUnonboarded queues a hard deletion of each Member left
	// unonboarded past its retention and returns how many it queued.
	EnqueueExpiredUnonboarded(ctx context.Context, now time.Time) (int, error)
	// DueScheduledDeletions lists the deletion requests whose grace period
	// ended before now.
	DueScheduledDeletions(ctx context.Context, now time.Time) ([]ScheduledDeletion, error)
	// DispatchScheduledDeletion queues the deletion commands of one
	// request; account.ErrLastActiveAdminDeletion blocks it.
	DispatchScheduledDeletion(ctx context.Context, requestID string, now time.Time) error
	// DeleteIdentity runs a queued identity deletion command.
	DeleteIdentity(ctx context.Context, command *managev1.UserDeleteIdentityCommand) error
	// DeleteAvatar runs a queued avatar deletion command.
	DeleteAvatar(ctx context.Context, memberID, avatarAssetID string) error
}

// IdentityLister pages through the Kratos identities.
type IdentityLister interface {
	ListIdentities(ctx context.Context, page, perPage int) ([]*auth.Identity, int64, error)
}

// TimedBans clears an expired timed ban after rechecking it.
type TimedBans interface {
	ClearExpiredTimedBan(ctx context.Context, identityID string, now time.Time) (bool, error)
}

// GeoIPRefresher refreshes the GeoIP database when it is due.
type GeoIPRefresher interface {
	Refresh(ctx context.Context, now time.Time) (geoip.RefreshOutcome, error)
}

// CampaignDeliveries dispatches the due Campaign delivery runs and resumes
// the active ones.
type CampaignDeliveries interface {
	DispatchDueEmailDeliveryRuns(ctx context.Context, limit int) (int, error)
	ResumeActiveEmailDeliveryRuns(ctx context.Context, limit int) (int, error)
}

// EmailDelivery delivers one transactional or Campaign recipient email.
type EmailDelivery interface {
	Deliver(ctx context.Context, job *managev1.SendEmailEvent) (emaildelivery.DeliveryResult, error)
}

// BulkEmail materializes one Campaign delivery batch into recipient emails.
type BulkEmail interface {
	Handle(ctx context.Context, job *managev1.SendBulkEmailBatchEvent) error
}

// MetadataAIJobProcessor runs one queued metadata AI job.
type MetadataAIJobProcessor interface {
	ProcessJob(ctx context.Context, jobID string) error
}

// TranslationJobProcessor runs one queued translation job.
type TranslationJobProcessor interface {
	ProcessDelivery(ctx context.Context, jobID string) error
}
