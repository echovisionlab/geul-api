package legal

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// InternalDeps are the collaborators of the collaboration use cases of
// Terms and Privacy documents. Every field is required; NewInternalService
// rejects a missing one.
type InternalDeps struct {
	DB            *gorm.DB
	ContentBlocks *contentblock.Store
	Permissions   CollaborationPermissionChecker
	Checkpoints   persistencecheckpoint.ContributorFence
	OG            OG
	Audit         domainaudit.Appender
}

func (d InternalDeps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "ContentBlocks": d.ContentBlocks == nil, "Permissions": d.Permissions == nil,
		"Checkpoints": d.Checkpoints == nil, "OG": d.OG == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("internal legal dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// InternalService holds the collaboration use cases of Terms and Privacy
// documents. Each takes the PolicyKind it acts on.
type InternalService struct {
	deps InternalDeps
}

// NewInternalService validates deps and returns the collaboration use cases.
func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &InternalService{deps: deps}, nil
}

func (s *InternalService) documents(kind PolicyKind) (internalLegalDocumentService, error) {
	if _, err := kind.descriptor(); err != nil {
		return internalLegalDocumentService{}, err
	}
	return internalLegalDocumentService{
		db: s.deps.DB, kind: string(kind), auditWriter: s.deps.Audit, contentBlocks: s.deps.ContentBlocks,
		spiceDB: s.deps.Permissions, checkpoints: s.deps.Checkpoints, legalOG: s.deps.OG,
	}, nil
}

// LoadBlockDocumentQuery reads a version's document for a collaboration
// principal, with the target locale's values when Locale is set.
type LoadBlockDocumentQuery struct {
	ID        string
	Locale    string
	Principal *intrav1.CollaborationPrincipal
}

// LoadBlockDocument returns the version's document for collaboration.
func (s *InternalService) LoadBlockDocument(ctx context.Context, kind PolicyKind, query LoadBlockDocumentQuery) (*BlockDocument, error) {
	documents, err := s.documents(kind)
	if err != nil {
		return nil, err
	}
	return documents.loadDocument(ctx, query.ID, query.Locale, query.Principal)
}

// ApplyBlockBatchCommand applies a collaboration Block batch to a version.
type ApplyBlockBatchCommand struct {
	ID                     string
	Locale                 string
	ExpectedTargetRevision *string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// ApplyBlockBatch applies the batch and returns what changed.
func (s *InternalService) ApplyBlockBatch(ctx context.Context, kind PolicyKind, command ApplyBlockBatchCommand) (*DocumentChange, error) {
	documents, err := s.documents(kind)
	if err != nil {
		return nil, err
	}
	return documents.applyDocumentMutation(ctx, command.ID, command.Locale, command.ExpectedTargetRevision, command.Batch, command.AffectedLocaleValues)
}

// UpdateLocaleMetadata changes a version's title in one locale.
func (s *InternalService) UpdateLocaleMetadata(ctx context.Context, kind PolicyKind, command UpdateLocaleMetadataCommand) (*DocumentChange, error) {
	documents, err := s.documents(kind)
	if err != nil {
		return nil, err
	}
	return documents.updateDocumentMetadata(ctx, command)
}
