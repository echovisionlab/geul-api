package accounttest

import (
	"context"
	"slices"
	"sync"

	"github.com/echovisionlab/geul-api/internal/account"
)

// Emails records the email commands Account publishes.
type Emails struct {
	mu   sync.Mutex
	sent []account.Email
	// Err fails every publish.
	Err error
}

var _ account.EmailPublisher = (*Emails)(nil)

// PublishEmail implements account.EmailPublisher.
func (e *Emails) PublishEmail(_ context.Context, job account.Email) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Err != nil {
		return e.Err
	}
	e.sent = append(e.sent, job)
	return nil
}

// Sent returns the published commands, in order.
func (e *Emails) Sent() []account.Email {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.sent)
}

// Members records the Member side of a deletion that runs outside an Account
// transaction.
type Members struct {
	mu       sync.Mutex
	revoked  []account.MemberUnonboardedDeletionTarget
	cleanups [][2]string
	// RevokeErr and CleanupErr fail the calls.
	RevokeErr, CleanupErr error
}

var _ account.MemberAuthorizations = (*Members)(nil)

// RevokeUnonboardedAuthorization implements account.MemberAuthorizations.
func (m *Members) RevokeUnonboardedAuthorization(_ context.Context, target account.MemberUnonboardedDeletionTarget) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.RevokeErr != nil {
		return m.RevokeErr
	}
	m.revoked = append(m.revoked, target)
	return nil
}

// CleanupAvatar implements account.MemberAuthorizations.
func (m *Members) CleanupAvatar(_ context.Context, memberID, assetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.CleanupErr != nil {
		return m.CleanupErr
	}
	m.cleanups = append(m.cleanups, [2]string{memberID, assetID})
	return nil
}

// Revoked returns the revoked hard-delete targets, in order.
func (m *Members) Revoked() []account.MemberUnonboardedDeletionTarget {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.revoked)
}

// Cleanups returns the avatar cleanups as Member and asset pairs, in order.
func (m *Members) Cleanups() [][2]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.cleanups)
}
