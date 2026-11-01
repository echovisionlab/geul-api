package legal

import (
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
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
	SpiceDB       *auth.SpiceDBClient
	Checkpoints   persistencecheckpoint.ContributorFence
	OG            OG
	Audit         domainaudit.Appender
}

func (d InternalDeps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "ContentBlocks": d.ContentBlocks == nil, "SpiceDB": d.SpiceDB == nil,
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
	db            *gorm.DB
	contentBlocks *contentblock.Store
	spiceDB       *auth.SpiceDBClient
	checkpoints   persistencecheckpoint.ContributorFence
	legalOG       OG
	mutations     *mutation.Runner
}

// NewInternalService validates deps and returns the collaboration use cases.
func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &InternalService{
		db: deps.DB, contentBlocks: deps.ContentBlocks, spiceDB: deps.SpiceDB,
		checkpoints: deps.Checkpoints, legalOG: deps.OG, mutations: mutations,
	}, nil
}

// LoadBlockDocumentQuery reads a version's document for a collaboration
// principal, with the target locale's values when Locale is set.
type LoadBlockDocumentQuery struct {
	ID        string
	Locale    string
	Principal *intrav1.CollaborationPrincipal
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
