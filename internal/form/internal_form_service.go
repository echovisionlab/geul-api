package form

import (
	"context"
	"errors"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// InternalFormService provides internal-only access for form document operations.
type InternalFormService struct {
	db             *gorm.DB
	asyncPublisher AsyncPublisher
	og             OG
	translation    Translation
	auditWriter    domainaudit.Appender
	spiceDB        *auth.SpiceDBClient
	authorization  formPermissionChecker
	contentBlocks  *contentblock.Store
}

func NewAuditedInternalFormService(
	db *gorm.DB,
	asyncPublisher AsyncPublisher,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
	deps Dependencies,
) *InternalFormService {
	if auditWriter == nil {
		panic("form mutation audit writer is required")
	}
	service := NewInternalFormService(db, asyncPublisher, spiceDB, deps)
	service.auditWriter = auditWriter
	return service
}

func (s *InternalFormService) appendFormCollaborativeLocaleContentAudit(
	ctx context.Context,
	tx *gorm.DB,
	contributorMemberID string,
	formID string,
	locale string,
) error {
	if s.auditWriter == nil {
		return errors.New("form mutation audit writer is required")
	}
	return domainaudit.AppendMember(
		ctx,
		tx,
		s.auditWriter,
		contributorMemberID,
		sharedtelemetry.AuditFormUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewFormLocaleContentAuditRecord(
				metadata, formID, locale, sharedtelemetry.AuditItemOperationUpdated,
			)
		},
	)
}

func NewInternalFormService(
	db *gorm.DB,
	asyncPublisher AsyncPublisher,
	spiceDB *auth.SpiceDBClient,
	deps Dependencies,
) *InternalFormService {
	dependencycheck.New("InternalFormService").
		RequireNotNil(db, "db").
		RequireNotNil(spiceDB, "spiceDB").
		RequireNotNil(deps.OG, "og").
		RequireNotNil(deps.Translation, "translation").
		RequireNotNil(deps.ContentBlocks, "contentBlocks").
		Validate()
	return &InternalFormService{
		db: db, asyncPublisher: asyncPublisher, og: deps.OG,
		translation: deps.Translation, spiceDB: spiceDB,
		contentBlocks: deps.ContentBlocks,
		authorization: newFormPermissionChecker(spiceDB, db),
	}
}
