package release

import (
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// Authorizer is the SpiceDB port of the Release use cases: the exact
// permission checks they decide and the relationship writes of the
// mutation pipeline. *auth.SpiceDBClient implements it.
type Authorizer = mutation.Authorizer

// Deps are the collaborators of the Release, Track, collaboration and AI
// document use cases; all are required.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // releasepostgres.NewUnits
	SpiceDB Authorizer
	Fence   identitystate.Fence
	Audit   domainaudit.Appender
	Members MemberSummaryLoader
	// TrackFiles aborts Track audio uploads outside any transaction.
	TrackFiles   TrackUploads
	Transcodes   TrackTranscodes
	WaveformJobs WaveformJobs
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
		"Members": d.Members == nil, "TrackFiles": d.TrackFiles == nil, "Transcodes": d.Transcodes == nil,
		"WaveformJobs": d.WaveformJobs == nil,
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
	units        mutation.UnitOfWork[Store]
	spiceDB      Authorizer
	members      MemberSummaryLoader
	trackFiles   TrackUploads
	transcodes   TrackTranscodes
	waveformJobs WaveformJobs
	mutations    *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Release use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, members: deps.Members, trackFiles: deps.TrackFiles,
		transcodes: deps.Transcodes, waveformJobs: deps.WaveformJobs, mutations: mutations,
	}, nil
}
