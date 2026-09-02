package form

import (
	"connectrpc.com/connect"
	"errors"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"gorm.io/gorm"
)

// FormService implements the FormService Connect handler
type FormService struct {
	managev1connect.UnimplementedFormServiceHandler
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	authorization  formPermissionChecker
	password       *crypto.PasswordHasher
	kratosClient   auth.IdentityManager
	assets         Assets
	og             OG
	routes         Routes
	securityAccess SecurityAccess
	translation    Translation
	auditWriter    domainaudit.Appender
	contentBlocks  *contentblock.Store
}

func securityAccessUnavailable() *connect.Error {
	return connect.NewError(connect.CodeUnavailable, errors.New("personal data access record is temporarily unavailable"))
}

func NewAuditedFormService(
	db *gorm.DB,
	password *crypto.PasswordHasher,
	kratosClient auth.IdentityManager,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	dependencies Dependencies,
) *FormService {
	if auditWriter == nil {
		panic("form domain audit writer is required")
	}
	if dependencies.SecurityAccess == nil {
		panic("form security access recorder is required")
	}
	service := NewFormService(db, password, kratosClient, spiceDB, dependencies)
	service.securityAccess = dependencies.SecurityAccess
	service.auditWriter = auditWriter
	return service
}

// NewFormService creates a new FormService
func NewFormService(
	db *gorm.DB,
	password *crypto.PasswordHasher,
	kratosClient auth.IdentityManager,
	spiceDB *auth.SpiceDBClient,
	dependencies Dependencies,
) *FormService {
	dependencycheck.New("FormService").
		RequireNotNil(db, "db").
		RequireNotNil(password, "password").
		RequireNotNil(kratosClient, "kratosClient").
		RequireNotNil(spiceDB, "spiceDB").
		RequireNotNil(dependencies.Assets, "assets").
		RequireNotNil(dependencies.OG, "og").
		RequireNotNil(dependencies.Routes, "routes").
		RequireNotNil(dependencies.Translation, "translation").
		RequireNotNil(dependencies.ContentBlocks, "contentBlocks").
		Validate()
	return &FormService{
		db:             db,
		spiceDB:        spiceDB,
		authorization:  newFormPermissionChecker(spiceDB, db),
		password:       password,
		kratosClient:   kratosClient,
		assets:         dependencies.Assets,
		og:             dependencies.OG,
		routes:         dependencies.Routes,
		translation:    dependencies.Translation,
		securityAccess: dependencies.SecurityAccess,
		contentBlocks:  dependencies.ContentBlocks,
	}
}
