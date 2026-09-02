//go:build integration

package application

import (
	"context"
	"sync"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

func (s *stubTranslationServicePublisher) PublishTranslationGenerate(
	_ context.Context,
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

func (s *stubTranslationServicePublisher) PublishContentUpdated(
	_ context.Context,
	event *managev1.ContentUpdatedEvent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.contentErr != nil {
		return s.contentErr
	}
	s.contentEvents = append(s.contentEvents, event)
	return nil
}
