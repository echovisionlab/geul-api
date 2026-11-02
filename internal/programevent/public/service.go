package public

import (
	"context"
	"errors"
	"slices"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/memberref"
)

// MediaHydrator resolves request-scoped delivery for authorized Program Event Block media.
type MediaHydrator interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error)
}

// CreditMemberSummaries projects public Member-owned identity details for Program Event credits.
type CreditMemberSummaries interface {
	LoadPublicCreditMemberSummaries(context.Context, []string) (map[string]*memberref.Summary, error)
}

// Deps are the collaborators of the public Program Event reads; all are
// required.
type Deps struct {
	Units         Units // programeventpublicpostgres.NewUnits
	Assets        Assets
	Files         MediaHydrator
	CreditMembers CreditMemberSummaries
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "Assets": d.Assets == nil,
		"Files": d.Files == nil, "CreditMembers": d.CreditMembers == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public program event dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the public Program Event, series and type reads.
type Service struct {
	units         Units
	assets        Assets
	files         MediaHydrator
	creditMembers CreditMemberSummaries
}

// NewService validates deps and returns the public Program Event reads.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, assets: deps.Assets, files: deps.Files, creditMembers: deps.CreditMembers,
	}, nil
}
