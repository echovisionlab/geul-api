package worker

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	filemediaapplication "github.com/echovisionlab/geul-api/internal/filemedia/application"
	"github.com/echovisionlab/geul-api/internal/mq"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// queuePorts records the domain call each queue message reaches.
type queuePorts struct {
	unreachable
	calls  *calls
	err    error
	result emaildelivery.DeliveryResult
}

func (q queuePorts) record(call string) error {
	q.calls.add(call)
	return q.err
}

func (q queuePorts) Deliver(_ context.Context, job *managev1.SendEmailEvent) (emaildelivery.DeliveryResult, error) {
	return q.result, q.record("Deliver " + job.GetMessageId())
}
func (q queuePorts) Handle(_ context.Context, job *managev1.SendBulkEmailBatchEvent) error {
	return q.record("Handle " + job.GetDeliveryRunId())
}
func (q queuePorts) HandleFileDelete(_ context.Context, event *managev1.FileDeleteEvent) error {
	return q.record("HandleFileDelete " + event.GetFileId())
}
func (q queuePorts) HandleTranscodeComplete(context.Context, []byte) error {
	return q.record("HandleTranscodeComplete")
}
func (q queuePorts) HandleWaveformComplete(context.Context, []byte) error {
	return q.record("HandleWaveformComplete")
}
func (q queuePorts) HandleWaveformFail(context.Context, []byte) error {
	return q.record("HandleWaveformFail")
}
func (q queuePorts) HandleMeshOptimizationComplete(context.Context, []byte) error {
	return q.record("HandleMeshOptimizationComplete")
}
func (q queuePorts) HandleMeshOptimizationFail(context.Context, []byte) error {
	return q.record("HandleMeshOptimizationFail")
}
func (q queuePorts) DeleteIdentity(_ context.Context, command *managev1.UserDeleteIdentityCommand) error {
	return q.record("DeleteIdentity " + command.GetMemberId())
}
func (q queuePorts) DeleteAvatar(_ context.Context, memberID, avatarAssetID string) error {
	return q.record("DeleteAvatar " + memberID + " " + avatarAssetID)
}
func (q queuePorts) ProcessJob(_ context.Context, jobID string) error {
	return q.record("ProcessJob " + jobID)
}

func newQueueHandlers(t *testing.T, ports queuePorts, metadataAI MetadataAIJobProcessor) *Handlers {
	t.Helper()
	return newTestHandlers(t, Deps{
		EmailDelivery: ports, BulkEmail: ports, FileMedia: ports, MemberDeletions: ports, MetadataAI: metadataAI,
	})
}

func marshal(t *testing.T, message proto.Message) []byte {
	t.Helper()
	body, err := proto.Marshal(message)
	require.NoError(t, err)
	return body
}

// queueOriginal is the original object of the queued File deletion.
func queueOriginal() *commonv1.MediaObjectTarget {
	return &commonv1.MediaObjectTarget{
		FileId: queueFileID, ObjectKey: "media/" + queueFileID + ".bin", Extension: "bin", MimeType: "application/octet-stream",
	}
}

const (
	queueMemberID   = "14600000-0000-1000-8000-000000000001"
	queueIdentityID = "14600000-0000-1000-8000-000000000002"
	queueFileID     = "14600000-0000-1000-8000-000000000003"
	queueAssetID    = "14600000-0000-1000-8000-000000000004"
)

func TestQueueMessagesReachTheirOwner(t *testing.T) {
	fileDelete := &managev1.FileDeleteEvent{FileId: queueFileID, Original: queueOriginal()}
	for name, tt := range map[string]struct {
		handle func(*Handlers) mq.Handler
		msg    mq.Message
		want   string
	}{
		"transactional email": {
			handle: func(h *Handlers) mq.Handler { return h.handleEmailSendMessage },
			msg:    mq.Message{Body: marshal(t, &managev1.SendEmailEvent{MessageId: new("message-1")}), MessageID: "message-1"},
			want:   "Deliver message-1",
		},
		"campaign batch": {
			handle: func(h *Handlers) mq.Handler { return h.handleCampaignEmailMessage },
			msg:    mq.Message{Body: marshal(t, &managev1.SendBulkEmailBatchEvent{DeliveryRunId: "run-1"}), MessageID: "run-1"},
			want:   "Handle run-1",
		},
		"file deletion": {
			handle: func(h *Handlers) mq.Handler { return h.handleFileDeleteMessage },
			msg:    mq.Message{Body: marshal(t, fileDelete), MessageID: queueFileID},
			want:   "HandleFileDelete " + queueFileID,
		},
		"transcode result": {
			handle: func(h *Handlers) mq.Handler { return h.handleTranscodeResultMessage },
			msg:    mq.Message{Body: marshal(t, &managev1.TranscodeCompleteEvent{EventId: "event-1"}), MessageID: "event-1"},
			want:   "HandleTranscodeComplete",
		},
		"completed waveform": {
			handle: func(h *Handlers) mq.Handler { return h.handleWaveformResultMessage },
			msg: mq.Message{Body: marshal(t, &managev1.WaveformResultEvent{Outcome: &managev1.WaveformResultEvent_Completed{
				Completed: &managev1.WaveformCompleteEvent{EventId: "event-1"},
			}}), MessageID: "event-1"},
			want: "HandleWaveformComplete",
		},
		"failed waveform": {
			handle: func(h *Handlers) mq.Handler { return h.handleWaveformResultMessage },
			msg: mq.Message{Body: marshal(t, &managev1.WaveformResultEvent{Outcome: &managev1.WaveformResultEvent_Failed{
				Failed: &managev1.WaveformFailEvent{EventId: "event-1"},
			}}), MessageID: "event-1"},
			want: "HandleWaveformFail",
		},
		"completed mesh optimization": {
			handle: func(h *Handlers) mq.Handler { return h.handleMeshOptimizationResultMessage },
			msg: mq.Message{Body: marshal(t, &managev1.MeshOptimizationResultEvent{Outcome: &managev1.MeshOptimizationResultEvent_Completed{
				Completed: &managev1.MeshOptimizationCompleteEvent{JobId: "job-1"},
			}}), MessageID: "job-1"},
			want: "HandleMeshOptimizationComplete",
		},
		"failed mesh optimization": {
			handle: func(h *Handlers) mq.Handler { return h.handleMeshOptimizationResultMessage },
			msg: mq.Message{Body: marshal(t, &managev1.MeshOptimizationResultEvent{Outcome: &managev1.MeshOptimizationResultEvent_Failed{
				Failed: &managev1.MeshOptimizationFailEvent{JobId: "job-1"},
			}}), MessageID: "job-1"},
			want: "HandleMeshOptimizationFail",
		},
		"identity deletion": {
			handle: func(h *Handlers) mq.Handler { return h.handleUserDeleteIdentityMessage },
			msg: mq.Message{Body: marshal(t, &managev1.UserDeleteIdentityCommand{
				MemberId: queueMemberID, IdentityId: queueIdentityID, Mode: managev1.UserDeleteIdentityMode_TOMBSTONE,
			}), MessageID: "user-delete-identity:" + queueMemberID},
			want: "DeleteIdentity " + queueMemberID,
		},
		"avatar deletion": {
			handle: func(h *Handlers) mq.Handler { return h.handleUserDeleteAvatarMessage },
			msg: mq.Message{Body: marshal(t, &managev1.UserDeleteAvatarCommand{
				MemberId: queueMemberID, AvatarAssetId: new(queueAssetID),
			}), MessageID: "user-delete-avatar:" + queueMemberID},
			want: "DeleteAvatar " + queueMemberID + " " + queueAssetID,
		},
		"metadata AI job": {
			handle: func(h *Handlers) mq.Handler { return h.handleAIMetadataGenerateMessage },
			msg:    mq.Message{Body: marshal(t, &managev1.MetadataGenerationQueueEvent{JobId: "job-1"}), MessageID: "job-1"},
			want:   "ProcessJob job-1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}}
			handlers := newQueueHandlers(t, ports, ports)

			require.NoError(t, tt.handle(handlers)(t.Context(), tt.msg))
			require.Equal(t, []string{tt.want}, ports.calls.all())

			t.Run("a transport ID that is not the payload's is poison", func(t *testing.T) {
				ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}}
				handlers := newQueueHandlers(t, ports, ports)
				msg := tt.msg
				msg.MessageID = "another-message"

				err := tt.handle(handlers)(t.Context(), msg)

				class, terminal := mq.TerminalDeliveryErrorClass(err)
				require.True(t, terminal)
				require.Equal(t, "transport_payload_id_mismatch", class)
				require.Empty(t, ports.calls.all(), "poison never reaches the owner")
			})

			t.Run("an undecodable payload is poison", func(t *testing.T) {
				ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}}
				handlers := newQueueHandlers(t, ports, ports)

				err := tt.handle(handlers)(t.Context(), mq.Message{Body: []byte{0xff, 0xff}, MessageID: tt.msg.MessageID})

				_, terminal := mq.TerminalDeliveryErrorClass(err)
				require.True(t, terminal, "%v", err)
				require.Empty(t, ports.calls.all())
			})
		})
	}
}

func TestOwnerFailuresStayRetryable(t *testing.T) {
	failed := errors.New("owner unavailable")
	ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}, err: failed}
	handlers := newQueueHandlers(t, ports, ports)

	err := handlers.handleFileDeleteMessage(t.Context(), mq.Message{
		Body: marshal(t, &managev1.FileDeleteEvent{FileId: queueFileID, Original: queueOriginal()}), MessageID: queueFileID,
	})

	require.ErrorIs(t, err, failed)
	_, terminal := mq.TerminalDeliveryErrorClass(err)
	require.False(t, terminal)
}

func TestFileDeleteOfAnInvalidTargetIsPoison(t *testing.T) {
	ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}, err: fmt.Errorf("check: %w", filemediaapplication.ErrInvalidFileDeleteTarget)}
	handlers := newQueueHandlers(t, ports, ports)

	err := handlers.handleFileDeleteMessage(t.Context(), mq.Message{
		Body: marshal(t, &managev1.FileDeleteEvent{FileId: queueFileID, Original: queueOriginal()}), MessageID: queueFileID,
	})

	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "invalid_file_delete_target", class)
}

func TestEmailSendMapsTheProviderOutcomeToTheQueueContract(t *testing.T) {
	failed := errors.New("provider rejected")
	for name, tt := range map[string]struct {
		result    emaildelivery.DeliveryResult
		terminal  bool
		wantClass string
	}{
		"accepted":  {result: emaildelivery.DeliveryResult{}},
		"retryable": {result: emaildelivery.DeliveryResult{Err: failed, Retryable: true, ErrorType: "rate_limited"}},
		"permanent": {
			result:    emaildelivery.DeliveryResult{Err: failed, ErrorType: managev1.EmailErrorType_EMAIL_ERROR_TYPE_INVALID_RECIPIENT.String()},
			terminal:  true,
			wantClass: "email_invalid_recipient",
		},
	} {
		t.Run(name, func(t *testing.T) {
			ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}, result: tt.result}
			handlers := newQueueHandlers(t, ports, nil)

			err := handlers.handleEmailSendMessage(t.Context(), mq.Message{
				Body: marshal(t, &managev1.SendEmailEvent{MessageId: new("message-1")}), MessageID: "message-1",
			})

			if tt.result.Err == nil {
				require.NoError(t, err)
				return
			}
			class, terminal := mq.TerminalDeliveryErrorClass(err)
			require.Equal(t, tt.terminal, terminal)
			require.Equal(t, tt.wantClass, class)
		})
	}
}

func TestMetadataAIJobWithoutTheProcessorIsRetried(t *testing.T) {
	ports := queuePorts{unreachable: unreachable{t: t}, calls: &calls{}}
	handlers := newQueueHandlers(t, ports, nil)

	err := handlers.handleAIMetadataGenerateMessage(t.Context(), mq.Message{
		Body: marshal(t, &managev1.MetadataGenerationQueueEvent{JobId: "job-1"}), MessageID: "job-1",
	})

	require.EqualError(t, err, "metadata AI job processor is not configured")
	_, terminal := mq.TerminalDeliveryErrorClass(err)
	require.False(t, terminal)
}

func TestTranslationJobWithoutTheProcessorIsPoison(t *testing.T) {
	handlers := newTestHandlers(t, Deps{})

	err := handlers.handleTranslationGenerateMessage(t.Context(), mq.Message{
		Body: marshal(t, &managev1.TranslationGenerateEvent{JobId: "job-1"}), MessageID: "job-1",
	})

	class, terminal := mq.TerminalDeliveryErrorClass(err)
	require.True(t, terminal)
	require.Equal(t, "translation_processor_unconfigured", class)
}

func TestRegisterQueuesConsumesTheOptionalQueuesOnlyWithTheirProcessors(t *testing.T) {
	required := []string{
		eventpkg.QueueUserDeleteIdentity, eventpkg.QueueUserDeleteAvatar, eventpkg.QueueFileDelete,
		eventpkg.QueueTranscodeResult, eventpkg.QueueWaveformResult, eventpkg.QueueMeshOptimizationResult,
		eventpkg.QueueEmailSend, eventpkg.QueueEmailAuth, eventpkg.QueueEmailCampaign,
	}

	manager := mq.NewConsumerManager(nil)
	require.NoError(t, newTestHandlers(t, Deps{}).RegisterQueues(manager))
	require.Equal(t, required, manager.QueueNames())

	manager = mq.NewConsumerManager(nil)
	require.NoError(t, newTestHandlers(t, Deps{
		MetadataAI: metadataAIProcessor{}, TranslationJobs: &recordingTranslationDeliveryProcessor{},
	}).RegisterQueues(manager))
	require.Equal(t, append(required, eventpkg.QueueAiMetadataGenerate, eventpkg.QueueTranslationGenerate), manager.QueueNames())
}

func TestNewHandlersNamesEachMissingCollaborator(t *testing.T) {
	_, err := NewHandlers(Deps{})

	require.EqualError(t, err, "worker dependencies are required: Units, FileMedia, DanglingFiles, PublicAssets, "+
		"OGCleanup, FaviconCleanup, Uploads, UploadStorage, FileIngest, ShareLinks, AuthCodes, QueueArchives, "+
		"MemberDeletions, Legal, Identities, TimedBans, GeoIP, Campaigns, EmailDelivery, BulkEmail")
}
