// Package audittest provides the audit appender tests use in place of the
// durable writer. Services require an appender; a test that does not assert
// audit records still passes a Recorder rather than nil.
package audittest

import (
	"context"
	"slices"
	"sync"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// Recorder keeps every domain audit and security access record in memory,
// or rejects every append with Err.
type Recorder struct {
	Err error

	mu       sync.Mutex
	domain   []sharedtelemetry.AuditRecord
	accessed []sharedtelemetry.SecurityAccessRecord
}

// AppendDomainAuditInTransaction implements domainaudit.Appender.
func (r *Recorder) AppendDomainAuditInTransaction(_ context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.domain = append(r.domain, record)
	return nil
}

// AppendSecurityAccess implements securityaccess.Appender.
func (r *Recorder) AppendSecurityAccess(_ context.Context, record sharedtelemetry.SecurityAccessRecord) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return r.Err
	}
	r.accessed = append(r.accessed, record)
	return nil
}

// Records returns the domain audit records appended so far. A record appended
// in a transaction that later rolled back stays here; assert the durable
// table when rollback matters.
func (r *Recorder) Records() []sharedtelemetry.AuditRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.domain)
}

// AccessRecords returns the security access records appended so far.
func (r *Recorder) AccessRecords() []sharedtelemetry.SecurityAccessRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.accessed)
}
