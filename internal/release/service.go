package release

import (
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
)

// Deps are the collaborators of the Release, Track, collaboration and AI
// document use cases; all are required.
type Deps struct {
	DB            *gorm.DB
	SpiceDB       *auth.SpiceDBClient
	Audit         domainaudit.Appender
	ContentBlocks *contentblock.Store
	// Checkpoints proves that collaboration contributors are still current.
	Checkpoints persistencecheckpoint.ContributorFence
	// ContentSignals schedules content.updated in a mutation's transaction.
	ContentSignals contentsignal.Publisher
	TrackFiles     TrackFileManager
	Transcodes     TrackTranscodes
	WaveformJobs   WaveformJobs
	Assets         Assets
	OG             OG
	ContentOG      ContentOG
	Members        MemberSummaryLoader
	Artists        ArtistSummaryLoader
	Labels         LabelSummaryLoader
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Audit": d.Audit == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Checkpoints": d.Checkpoints == nil,
		"ContentSignals": d.ContentSignals == nil, "TrackFiles": d.TrackFiles == nil,
		"Transcodes": d.Transcodes == nil, "WaveformJobs": d.WaveformJobs == nil,
		"Assets": d.Assets == nil, "OG": d.OG == nil, "ContentOG": d.ContentOG == nil,
		"Members": d.Members == nil, "Artists": d.Artists == nil, "Labels": d.Labels == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("release dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Release and Track use cases of administrators, of the
// collaboration service and of AI document editing. Connect and MCP adapters
// translate their requests into its commands and queries.
type Service struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	contentBlocks  *contentblock.Store
	checkpoints    persistencecheckpoint.ContributorFence
	contentSignals contentsignal.Publisher
	trackFiles     TrackFileManager
	transcodes     TrackTranscodes
	waveformJobs   WaveformJobs
	assets         Assets
	og             OG
	contentOG      ContentOG
	members        MemberSummaryLoader
	artists        ArtistSummaryLoader
	labels         LabelSummaryLoader
	mutations      *mutation.Runner
}

// NewService validates deps and returns the Release use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, contentBlocks: deps.ContentBlocks,
		checkpoints: deps.Checkpoints, contentSignals: deps.ContentSignals, trackFiles: deps.TrackFiles,
		transcodes: deps.Transcodes, waveformJobs: deps.WaveformJobs, assets: deps.Assets, og: deps.OG,
		contentOG: deps.ContentOG, members: deps.Members, artists: deps.Artists, labels: deps.Labels,
		mutations: mutations,
	}, nil
}
