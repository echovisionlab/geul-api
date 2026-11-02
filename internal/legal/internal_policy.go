package legal

import (
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// InternalDeps are the collaborators of the collaboration use cases of
// Terms and Privacy documents. Every field is required; NewInternalService
// rejects a missing one.
type InternalDeps struct {
	Units   mutation.UnitOfWork[Store] // legalpostgres.NewUnits
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
}

func (d InternalDeps) validate() error {
	return requireDeps("internal legal dependencies are required: ", map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
	})
}

// InternalService holds the collaboration use cases of Terms and Privacy
// documents. Each takes the PolicyKind it acts on.
type InternalService struct {
	units     mutation.UnitOfWork[Store]
	spiceDB   mutation.Authorizer
	mutations *mutation.StoreRunner[Store]
}

// NewInternalService validates deps and returns the collaboration use cases.
func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &InternalService{units: deps.Units, spiceDB: deps.SpiceDB, mutations: mutations}, nil
}

// LoadBlockDocumentQuery reads a version's document for a collaboration
// principal, with the target locale's values when Locale is set.
type LoadBlockDocumentQuery struct {
	ID     string
	Locale string
	// SessionID is the collaboration principal's browser session.
	SessionID string
}

// ApplyBlockBatchCommand applies a collaboration Block batch to a version.
type ApplyBlockBatchCommand struct {
	ID                     string
	Locale                 string
	ExpectedTargetRevision *string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// UpdateLocaleMetadataCommand changes a version's title in one locale.
type UpdateLocaleMetadataCommand struct {
	EntityID               string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	Title                  *string
	Contributors           []string
}

// LocaleMetadata is a version's title in one locale.
type LocaleMetadata struct {
	Locale string
	Title  *string
}

// BlockDocument is a version's document as collaboration loads it.
type BlockDocument struct {
	Document       *contentv1.LocalizedRichTextDocument
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata LocaleMetadata
	Locale         string
	LocaleExists   bool
	LocaleMetadata *LocaleMetadata
	TargetRevision *string
}

// DocumentChange is what a collaboration write changed.
type DocumentChange struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	Locale           string
	TargetRevision   *string
}
