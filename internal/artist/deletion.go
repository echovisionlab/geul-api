package artist

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

type artistDeletionImpact struct {
	Domain        managev1.ArtistRelationDomain
	EntityID      string
	Label         string
	RelationCount int32
}

// PreviewDeleteArtistQuery reads what deleting an Artist would detach.
type PreviewDeleteArtistQuery struct {
	ID string
}

// PreviewDeleteArtist lists the child Artists, Labels, Program Events,
// Releases, Tracks and Works that reference the Artist, with the revision
// DeleteArtist checks.
func (s *Service) PreviewDeleteArtist(ctx context.Context, query PreviewDeleteArtistQuery) (*managev1.PreviewDeleteArtistResponse, error) {
	if err := artistEntity.RequirePermission(ctx, s.spiceDB, query.ID, policyv1.Artist.Delete); err != nil {
		return nil, err
	}
	var impacts []artistDeletionImpact
	if err := mutation.Read(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		impacts, err = s.loadArtistDeletionImpacts(ctx, tx, query.ID)
		return err
	}); err != nil {
		return nil, err
	}
	return buildArtistDeletionPreview(impacts), nil
}

// loadArtistDeletionImpacts lists what references the Artist, ordered by
// relation domain and entity ID.
func (s *Service) loadArtistDeletionImpacts(ctx context.Context, db *gorm.DB, artistID string) ([]artistDeletionImpact, error) {
	impacts := make([]artistDeletionImpact, 0)
	for _, related := range []struct {
		domain managev1.ArtistRelationDomain
		load   func(context.Context, *gorm.DB, string) ([]entityparticipant.Related, error)
	}{
		{domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_CHILD_ARTIST, load: loadChildArtists},
		{domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_LABEL, load: s.loadArtistLabels},
		{domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_PROGRAM_EVENT, load: s.programEvents.ProgramEventsOfArtist},
		{domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_RELEASE, load: s.releases.ReleasesOfArtist},
		{domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_TRACK, load: s.releases.TracksOfArtist},
		{domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_WORK, load: s.works.WorksOfArtist},
	} {
		rows, err := related.load(ctx, db, artistID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			impacts = append(impacts, artistDeletionImpact{
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

func loadChildArtists(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error) {
	var rows []entityparticipant.Related
	err := db.WithContext(ctx).Raw(`
		SELECT child.id::text AS id,
		       `+ArtistSourceTitleSQL("child")+` AS display_name,
		       1::integer AS relation_count
		FROM artist AS child
		WHERE child.parent_artist_id = ?::uuid
	`, artistID).Scan(&rows).Error
	return rows, err
}

// loadArtistLabels lists the Labels the Artist links with their source
// titles.
func (s *Service) loadArtistLabels(ctx context.Context, db *gorm.DB, artistID string) ([]entityparticipant.Related, error) {
	var labelIDs []string
	if err := db.WithContext(ctx).Table("artist_label").Select("label_id::text").Where("artist_id = ?::uuid", artistID).Scan(&labelIDs).Error; err != nil {
		return nil, err
	}
	titles, err := s.labels.LabelTitles(ctx, db, labelIDs)
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

func artistDeletionRevision(impacts []artistDeletionImpact) string {
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

func buildArtistDeletionPreview(impacts []artistDeletionImpact) *managev1.PreviewDeleteArtistResponse {
	response := &managev1.PreviewDeleteArtistResponse{
		Revision: artistDeletionRevision(impacts),
		Impacts:  make([]*managev1.ArtistDeletionImpact, 0, len(impacts)),
	}
	for _, impact := range impacts {
		response.TotalRelationCount += impact.RelationCount
		response.Impacts = append(response.Impacts, &managev1.ArtistDeletionImpact{
			Domain:        impact.Domain,
			EntityId:      impact.EntityID,
			Label:         impact.Label,
			RelationCount: impact.RelationCount,
		})
	}
	return response
}
