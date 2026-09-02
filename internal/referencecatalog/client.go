package referencecatalog

import (
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	managev1connect "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"gorm.io/gorm"
)

// ClientService implements the ClientService Connect handler
type ClientService struct {
	managev1connect.UnimplementedClientServiceHandler
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	assets      Assets
	auditWriter domainaudit.Appender
}

// NewClientService creates a new ClientService
func NewClientService(db *gorm.DB, assets Assets, spiceDB *auth.SpiceDBClient) *ClientService {
	if db == nil {
		panic("db is required")
	}
	if assets == nil {
		panic("assets are required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &ClientService{
		db:      db,
		spiceDB: spiceDB,
		assets:  assets,
	}
}

// NewAuditedClientService creates a ClientService whose mutations append their
// Domain Audit record in the same transaction as the authoritative state.
func NewAuditedClientService(db *gorm.DB, auditWriter domainaudit.Appender, assets Assets, spiceDB *auth.SpiceDBClient) *ClientService {
	if auditWriter == nil {
		panic("client audit writer is required")
	}
	service := NewClientService(db, assets, spiceDB)
	service.auditWriter = auditWriter
	return service
}
