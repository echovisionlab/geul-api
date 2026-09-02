package referencecatalog

import (
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

var mapPlaceSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "updated_at DESC",
}

// MapPlaceService implements the MapPlaceService RPC service
type MapPlaceService struct {
	managev1connect.UnimplementedMapPlaceServiceHandler
	db          *gorm.DB
	assets      Assets
	members     MemberSummaries
	spiceDB     *auth.SpiceDBClient
	auditWriter domainaudit.Appender
}

// NewMapPlaceService creates a new MapPlaceService
func NewMapPlaceService(db *gorm.DB, assets Assets, members MemberSummaries, spiceDB *auth.SpiceDBClient) *MapPlaceService {
	if db == nil {
		panic("db is required")
	}
	if assets == nil {
		panic("assets are required")
	}
	if members == nil {
		panic("member summaries are required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	return &MapPlaceService{
		db:      db,
		assets:  assets,
		members: members,
		spiceDB: spiceDB,
	}
}

// NewAuditedMapPlaceService makes Map Place mutations fail closed with their
// Domain Audit records in the same authoritative database transaction.
func NewAuditedMapPlaceService(db *gorm.DB, auditWriter domainaudit.Appender, assets Assets, members MemberSummaries, spiceDB *auth.SpiceDBClient) *MapPlaceService {
	if auditWriter == nil {
		panic("map place audit writer is required")
	}
	service := NewMapPlaceService(db, assets, members, spiceDB)
	service.auditWriter = auditWriter
	return service
}
