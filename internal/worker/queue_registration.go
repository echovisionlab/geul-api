package worker

import (
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mq/middleware"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// RegisterQueues registers all queue handlers with the consumer manager
func (h *Handlers) RegisterQueues(manager *mq.ConsumerManager) error {
	// Standard middleware chain applied to all handlers
	baseMiddlewares := []mq.Middleware{
		middleware.Recovery(),
	}

	for _, deletionQueue := range []struct {
		config  mq.QueueConfig
		handler mq.Handler
	}{
		{config: userDeletionQueueConfig(eventpkg.QueueUserDeleteIdentity), handler: h.handleUserDeleteIdentityMessage},
		{config: userDeletionQueueConfig(eventpkg.QueueUserDeleteAvatar), handler: h.handleUserDeleteAvatarMessage},
	} {
		if err := manager.Register(deletionQueue.config, deletionQueue.handler, baseMiddlewares...); err != nil {
			return fmt.Errorf("failed to register %s queue: %w", deletionQueue.config.Name, err)
		}
	}

	// File delete queue - S3 deletions
	// Direct PGMQ work queue consumption.
	if err := manager.Register(
		mq.QueueConfig{
			Name:         eventpkg.QueueFileDelete,
			MessageType:  "api.manage.v1.FileDeleteEvent",
			Workers:      3,
			Timeout:      30 * time.Second,
			MaxRetries:   2,
			RetryDelay:   500 * time.Millisecond,
			RetryBackoff: 1.5,
		},
		h.handleFileDeleteMessage,
		baseMiddlewares...,
	); err != nil {
		return fmt.Errorf("failed to register file.delete queue: %w", err)
	}

	for _, resultQueue := range []struct {
		name        string
		messageType string
		handler     mq.Handler
	}{
		{name: eventpkg.QueueTranscodeResult, messageType: "api.manage.v1.TranscodeCompleteEvent", handler: h.handleTranscodeResultMessage},
		{name: eventpkg.QueueWaveformResult, messageType: "api.manage.v1.WaveformResultEvent", handler: h.handleWaveformResultMessage},
		{name: eventpkg.QueueMeshOptimizationResult, messageType: "api.manage.v1.MeshOptimizationResultEvent", handler: h.handleMeshOptimizationResultMessage},
	} {
		if err := manager.Register(
			mq.QueueConfig{
				Name:         resultQueue.name,
				MessageType:  resultQueue.messageType,
				Workers:      3,
				Timeout:      2 * time.Minute,
				MaxRetries:   mediaResultMaxRetries,
				RetryDelay:   mediaResultRetryDelay,
				RetryBackoff: 2,
			},
			resultQueue.handler,
			baseMiddlewares...,
		); err != nil {
			return fmt.Errorf("failed to register %s queue: %w", resultQueue.name, err)
		}
	}

	// Email send queue - transactional emails
	// Direct PGMQ work queue consumption.
	if err := manager.Register(
		mq.QueueConfig{
			Name:         eventpkg.QueueEmailSend,
			MessageType:  "api.manage.v1.SendEmailEvent",
			Workers:      10,
			Timeout:      2 * time.Minute,
			MaxRetries:   emailSendMaxRetries,
			RetryDelay:   emailSendRetryDelay,
			RetryBackoff: emailSendRetryBackoff,
		},
		h.handleEmailSendMessage,
		baseMiddlewares...,
	); err != nil {
		return fmt.Errorf("failed to register email.send queue: %w", err)
	}

	// Authentication email queue - reserved capacity for short-lived codes.
	// Its bounded retry window is deliberately shorter than the code lifetime.
	if err := manager.Register(
		authEmailQueueConfig(),
		h.handleEmailSendMessage,
		baseMiddlewares...,
	); err != nil {
		return fmt.Errorf("failed to register email.auth queue: %w", err)
	}

	if err := manager.Register(
		campaignEmailQueueConfig(),
		h.handleCampaignEmailMessage,
		baseMiddlewares...,
	); err != nil {
		return fmt.Errorf("failed to register email.campaign queue: %w", err)
	}

	if h.metadataAI != nil {
		if err := manager.Register(
			mq.QueueConfig{
				Name:         eventpkg.QueueAiMetadataGenerate,
				MessageType:  "api.manage.v1.MetadataGenerationQueueEvent",
				Workers:      2,
				Timeout:      2 * time.Minute,
				MaxRetries:   0,
				RetryDelay:   time.Second,
				RetryBackoff: 1.0,
			},
			h.handleAIMetadataGenerateMessage,
			baseMiddlewares...,
		); err != nil {
			return fmt.Errorf("failed to register ai.metadata.generate queue: %w", err)
		}
	}

	if h.translationJobs != nil {
		if err := manager.Register(
			translationGenerateQueueConfig(),
			h.handleTranslationGenerateMessage,
			baseMiddlewares...,
		); err != nil {
			return fmt.Errorf("failed to register translation generate queue: %w", err)
		}
	}

	return nil
}

func authEmailQueueConfig() mq.QueueConfig {
	return mq.QueueConfig{
		Name:         eventpkg.QueueEmailAuth,
		MessageType:  "api.manage.v1.SendEmailEvent",
		Workers:      4,
		Timeout:      45 * time.Second,
		MaxRetries:   authEmailMaxRetries,
		RetryDelay:   authEmailRetryDelay,
		RetryBackoff: emailSendRetryBackoff,
	}
}

func campaignEmailQueueConfig() mq.QueueConfig {
	return mq.QueueConfig{
		Name:         eventpkg.QueueEmailCampaign,
		MessageType:  "api.manage.v1.SendBulkEmailBatchEvent",
		Workers:      5,
		Timeout:      5 * time.Minute,
		MaxRetries:   3,
		RetryDelay:   time.Second,
		RetryBackoff: 2,
	}
}

func userDeletionQueueConfig(name string) mq.QueueConfig {
	messageType := map[string]string{
		eventpkg.QueueUserDeleteIdentity: "api.manage.v1.UserDeleteIdentityCommand",
		eventpkg.QueueUserDeleteAvatar:   "api.manage.v1.UserDeleteAvatarCommand",
	}[name]
	return mq.QueueConfig{
		Name:         name,
		MessageType:  messageType,
		Workers:      3,
		Timeout:      time.Minute,
		MaxRetries:   userDeletionMaxRetries,
		RetryDelay:   userDeletionRetryDelay,
		RetryBackoff: 2,
	}
}

func terminalQueueContractError(class string, err error) error {
	return mq.NewTerminalDeliveryError(class, err)
}

func requireStableDeliveryMessageID(msg mq.Message, expected string) error {
	messageID := strings.TrimSpace(msg.MessageID)
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return terminalQueueContractError(
			"missing_payload_message_id",
			fmt.Errorf("durable payload stable id is required"),
		)
	}
	if messageID == "" {
		return terminalQueueContractError(
			"missing_transport_message_id",
			fmt.Errorf("transport message id is required for stable payload %s", expected),
		)
	}
	if messageID != expected {
		return terminalQueueContractError(
			"transport_payload_id_mismatch",
			fmt.Errorf("transport message id %q does not match payload stable id %q", messageID, expected),
		)
	}
	return nil
}
