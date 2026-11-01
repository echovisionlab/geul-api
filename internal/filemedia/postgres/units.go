// Package filemediapostgres implements the File Store against PostgreSQL.
package filemediapostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"gorm.io/gorm"
)

type DeletionQueue interface {
	EnqueueFileDeletion(context.Context, eventpkg.DBTX, domain.DeletionTarget) error
}
type Ports struct {
	UploadWorks      UploadWorks
	MeshJobs         MeshJobs
	IngestTranscodes IngestTranscodes
	UploadTracks     UploadTracks
	DeletionQueue    DeletionQueue
	PolicyOwners     PolicyOwners
	PolicyAudiences  PolicyAudiences
	PolicySignals    PolicySignals
}
type store struct {
	tx    *gorm.DB
	ports Ports
}

func NewUnits(db *gorm.DB, ports Ports) mutation.UnitOfWork[domain.Store] {
	if db == nil {
		panic("file media units: db is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) domain.Store { return NewStore(tx, ports) })
}
func NewStore(tx *gorm.DB, ports Ports) domain.Store { return &store{tx: tx, ports: ports} }

type storeError struct {
	error
	kind error
}

func (e storeError) Is(target error) bool { return target == e.kind }
func (e storeError) Unwrap() error        { return e.error }

func Bind(tx *gorm.DB, ports Ports, audit domainaudit.Appender) domain.Joined {
	return domain.Joined{Store: NewStore(tx, ports), Audit: domainaudit.Bind(tx, audit)}
}
