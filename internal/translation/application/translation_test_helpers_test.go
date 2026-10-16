//go:build integration

package application

import (
	"context"
	"fmt"
	"sync"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type stubTranslationServicePublisher struct {
	mu              sync.Mutex
	generateEvents  []*managev1.TranslationGenerateEvent
	generateErrors  []error
	lifecycleEvents []*managev1.TranslationLifecycleEvent
	lifecycleErr    error
	contentEvents   []*managev1.ContentUpdatedEvent
	contentErr      error
}

func (s *stubTranslationServicePublisher) EnqueueTranslationGenerateWithDB(
	_ context.Context,
	_ *gorm.DB,
	event *managev1.TranslationGenerateEvent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generateEvents = append(s.generateEvents, event)
	index := len(s.generateEvents) - 1
	if index < len(s.generateErrors) {
		return s.generateErrors[index]
	}
	return nil
}

func (s *stubTranslationServicePublisher) PublishTranslationLifecycle(
	_ context.Context,
	event *managev1.TranslationLifecycleEvent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lifecycleErr != nil {
		return s.lifecycleErr
	}
	s.lifecycleEvents = append(s.lifecycleEvents, event)
	return nil
}

func (s *stubTranslationServicePublisher) NotifyProtobufWithExecutor(
	_ context.Context,
	_ eventpkg.DBTX,
	signal string,
	message proto.Message,
) error {
	event, ok := message.(*managev1.ContentUpdatedEvent)
	if signal != eventpkg.SignalContentUpdated || !ok {
		return fmt.Errorf("unexpected transactional signal %q", signal)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contentErr != nil {
		return s.contentErr
	}
	s.contentEvents = append(s.contentEvents, event)
	return nil
}
