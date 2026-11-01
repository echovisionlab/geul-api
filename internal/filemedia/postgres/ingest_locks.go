package filemediapostgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/postgreslock"
	"gorm.io/gorm"
)

var multipartLockSlots = make(chan struct{}, 2)
var remoteImportLockSlots = make(chan struct{}, 2)

type ingestLocks struct {
	db    *gorm.DB
	ports Ports
}

func NewIngestLocks(db *gorm.DB, ports Ports) domain.IngestLocks {
	if db == nil {
		panic("File ingest locks: db is required")
	}
	return ingestLocks{db, ports}
}
func (l ingestLocks) Multipart(ctx context.Context, upload, file string, work func(domain.IngestSession) error) error {
	return l.lock(ctx, ingestLockKey("multipart-completion:"+strings.TrimSpace(upload)+"\x00"+strings.TrimSpace(file)), multipartLockSlots, "multipart completion", work)
}
func (l ingestLocks) RemoteImport(ctx context.Context, file string, work func(domain.IngestSession) error) error {
	return l.lock(ctx, ingestLockKey(file), remoteImportLockSlots, "remote import", work)
}
func ingestLockKey(identity string) int64 {
	digest := sha256.Sum256([]byte(strings.TrimSpace(identity)))
	return int64(binary.BigEndian.Uint64(digest[:8]))
}
func (l ingestLocks) lock(ctx context.Context, key int64, slots chan struct{}, scope string, work func(domain.IngestSession) error) error {
	if work == nil {
		return fmt.Errorf("%s operation is required", scope)
	}
	return postgreslock.WithBoundedAdvisoryLock(ctx, l.db, key, slots, scope, func(connection *gorm.DB) error {
		return work(domain.IngestSession{Units: NewUnits(connection, l.ports), FaviconCommits: NewFaviconCommits(connection, l.ports)})
	})
}
