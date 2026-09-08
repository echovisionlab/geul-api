// Package app provides shared service lifecycle and HTTP runtime helpers.
package mediaruntime

import (
	"context"
	"fmt"
)

// Starter starts a long-running service component.
type Starter interface {
	Start(context.Context) error
}

// Closer releases a service component.
type Closer interface {
	Close() error
}

// HealthSource reports whether a service dependency is currently ready.
type HealthSource interface {
	Healthy() bool
}

// Service combines startup, shutdown, and health behavior.
type Service interface {
	Start(context.Context) error
	Close() error
	Healthy() bool
}

// Group coordinates the lifecycle of service components.
type Group struct {
	health   []HealthSource
	starters []Starter
	closers  []Closer
}

// NewGroup creates a lifecycle group with one health source.
func NewGroup(health HealthSource, starters []Starter, closers []Closer) (*Group, error) {
	if health == nil {
		return nil, fmt.Errorf("health source is required")
	}
	return &Group{health: []HealthSource{health}, starters: starters, closers: closers}, nil
}

// AddHealthSource adds a dependency that must remain ready for the service to
// receive work correctly.
func (g *Group) AddHealthSource(health HealthSource) {
	if health != nil {
		g.health = append(g.health, health)
	}
}

// Start starts each component in declaration order.
func (g *Group) Start(ctx context.Context) error {
	for _, starter := range g.starters {
		if starter == nil {
			return fmt.Errorf("service starter is required")
		}
		if err := starter.Start(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close releases components in reverse declaration order.
func (g *Group) Close() error {
	return CloseAll(g.closers)
}

// CloseAll releases resources in reverse order and returns the first error.
func CloseAll(closers []Closer) error {
	var firstErr error
	for index := len(closers) - 1; index >= 0; index-- {
		if closers[index] == nil {
			continue
		}
		if err := closers[index].Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Healthy reports whether the group's health source is open.
func (g *Group) Healthy() bool {
	for _, health := range g.health {
		if !health.Healthy() {
			return false
		}
	}
	return true
}
