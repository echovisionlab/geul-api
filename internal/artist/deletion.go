package artist

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/echovisionlab/geul-api/internal/domainerr"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// RelationDomain names what references an Artist a deletion detaches. The
// values are those of managev1.ArtistRelationDomain.
type RelationDomain int32

const (
	RelationChildArtist  RelationDomain = 1
	RelationLabel        RelationDomain = 2
	RelationProgramEvent RelationDomain = 3
	RelationRelease      RelationDomain = 4
	RelationTrack        RelationDomain = 5
	RelationWork         RelationDomain = 6
)

// DeletionImpact is one row that references the Artist.
type DeletionImpact struct {
	Domain        RelationDomain
	EntityID      string
	Label         string
	RelationCount int32
}

// DeletionPreview is what deleting an Artist would detach, with the revision
// DeleteArtist checks.
type DeletionPreview struct {
	Revision string
	Impacts  []DeletionImpact
}

// PreviewDeleteArtistQuery reads what deleting an Artist would detach.
type PreviewDeleteArtistQuery struct {
	ID string
}

// PreviewDeleteArtist lists the child Artists, Labels, Program Events,
// Releases, Tracks and Works that reference the Artist, with the revision
// DeleteArtist checks.
func (s *Service) PreviewDeleteArtist(ctx context.Context, query PreviewDeleteArtistQuery) (DeletionPreview, error) {
	if err := artistEntity.RequirePermission(ctx, s.spiceDB, query.ID, policyv1.Artist.Delete); err != nil {
		return DeletionPreview{}, err
	}
	var impacts []DeletionImpact
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		impacts, err = loadArtistDeletionImpacts(ctx, store, query.ID)
		return err
	}); err != nil {
		return DeletionPreview{}, err
	}
	return DeletionPreview{Revision: artistDeletionRevision(impacts), Impacts: impacts}, nil
}

// loadArtistDeletionImpacts lists what references the Artist, ordered by
// relation domain and entity ID.
func loadArtistDeletionImpacts(ctx context.Context, store Store, artistID string) ([]DeletionImpact, error) {
	impacts := make([]DeletionImpact, 0)
	for _, related := range []struct {
		domain RelationDomain
		load   func(context.Context, string) ([]entityparticipant.Related, error)
	}{
		{domain: RelationChildArtist, load: store.ChildArtists},
		{domain: RelationLabel, load: func(ctx context.Context, artistID string) ([]entityparticipant.Related, error) {
			return loadArtistLabels(ctx, store, artistID)
		}},
		{domain: RelationProgramEvent, load: store.ProgramEventsOfArtist},
		{domain: RelationRelease, load: store.ReleasesOfArtist},
		{domain: RelationTrack, load: store.TracksOfArtist},
		{domain: RelationWork, load: store.WorksOfArtist},
	} {
		rows, err := related.load(ctx, artistID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			impacts = append(impacts, DeletionImpact{
				Domain: related.domain, EntityID: row.ID, Label: row.DisplayName, RelationCount: row.RelationCount,
			})
		}
	}
	sort.Slice(impacts, func(i, j int) bool {
		if impacts[i].Domain != impacts[j].Domain {
			return impacts[i].Domain < impacts[j].Domain
		}
		return impacts[i].EntityID < impacts[j].EntityID
	})
	return impacts, nil
}

// loadArtistLabels lists the Labels the Artist links with their source
// titles.
func loadArtistLabels(ctx context.Context, store Store, artistID string) ([]entityparticipant.Related, error) {
	labelIDs, err := store.LabelIDs(ctx, artistID)
	if err != nil {
		return nil, err
	}
	titles, err := store.LabelTitles(ctx, labelIDs)
	if err != nil {
		return nil, err
	}
	rows := make([]entityparticipant.Related, 0, len(labelIDs))
	for _, labelID := range labelIDs {
		if title, exists := titles[labelID]; exists {
			rows = append(rows, entityparticipant.Related{ID: labelID, DisplayName: title, RelationCount: 1})
		}
	}
	return rows, nil
}

func artistDeletionRevision(impacts []DeletionImpact) string {
	hash := sha256.New()
	var number [4]byte
	for _, impact := range impacts {
		binary.BigEndian.PutUint32(number[:], uint32(impact.Domain))
		_, _ = hash.Write(number[:])
		binary.BigEndian.PutUint32(number[:], uint32(len(impact.EntityID)))
		_, _ = hash.Write(number[:])
		_, _ = hash.Write([]byte(impact.EntityID))
		binary.BigEndian.PutUint32(number[:], uint32(impact.RelationCount))
		_, _ = hash.Write(number[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validateResourceDeletionAuthorizationBatchSize(resourceName string, deleted, restored []policyv1.RelationshipMutation) error {
	const maxMutations = 1000
	if len(deleted) <= maxMutations && len(restored) <= maxMutations {
		return nil
	}
	return domainerr.Precondition(fmt.Sprintf("%s has too many authorization relationships to delete atomically; remove participant relationships or reparent dependent resources first", resourceName))
}
