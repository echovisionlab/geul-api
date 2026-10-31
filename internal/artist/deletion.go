package artist

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type artistDeletionImpact struct {
	Domain        managev1.ArtistRelationDomain
	EntityID      string
	Label         string
	RelationCount int32
}

type artistDeletionImpactRow struct {
	Domain        string `gorm:"column:domain"`
	EntityID      string `gorm:"column:entity_id"`
	Label         string `gorm:"column:label"`
	RelationCount int32  `gorm:"column:relation_count"`
}

func (s *ArtistService) PreviewDeleteArtist(
	ctx context.Context,
	req *connect.Request[managev1.PreviewDeleteArtistRequest],
) (*connect.Response[managev1.PreviewDeleteArtistResponse], error) {
	if err := requireArtistPermission(ctx, s.spiceDB, req.Msg.Id, policyv1.Artist.Delete); err != nil {
		return nil, err
	}
	impacts, err := loadArtistDeletionImpacts(ctx, s.db, req.Msg.Id)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(buildArtistDeletionPreview(impacts)), nil
}

func loadArtistDeletionImpacts(ctx context.Context, db *gorm.DB, artistID string) ([]artistDeletionImpact, error) {
	queries := []struct {
		domain managev1.ArtistRelationDomain
		sql    string
	}{
		{
			domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_CHILD_ARTIST,
			sql: `
				SELECT child.id::text AS entity_id,
				       ` + ArtistSourceTitleSQL("child") + ` AS label,
				       1::integer AS relation_count
				FROM artist AS child
				WHERE child.parent_artist_id = ?::uuid
			`,
		},
		{
			domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_LABEL,
			sql: `
				SELECT label.id::text AS entity_id,
				       ` + LabelSourceTitleSQL("label") + ` AS label,
				       1::integer AS relation_count
				FROM artist_label
				JOIN label ON label.id = artist_label.label_id
				WHERE artist_label.artist_id = ?::uuid
			`,
		},
		{
			domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_PROGRAM_EVENT,
			sql: `
				SELECT program_event.id::text AS entity_id,
				       program_event.title AS label,
				       COUNT(*)::integer AS relation_count
				FROM (
					SELECT event_id FROM program_event_artist WHERE artist_id = ?::uuid
					UNION ALL
					SELECT event_id FROM program_event_credit WHERE artist_id = ?::uuid
				) AS relation
				JOIN program_event ON program_event.id = relation.event_id
				GROUP BY program_event.id, program_event.title
			`,
		},
		{
			domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_RELEASE,
			sql: `
				SELECT release.id::text AS entity_id,
				       ` + ReleaseSourceTitleSQL("release") + ` AS label,
				       COUNT(*)::integer AS relation_count
				FROM (
					SELECT release_id FROM release_artist WHERE artist_id = ?::uuid
					UNION ALL
					SELECT release_id FROM release_credit WHERE artist_id = ?::uuid
				) AS relation
				JOIN release ON release.id = relation.release_id
				GROUP BY release.id
			`,
		},
		{
			domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_TRACK,
			sql: `
				SELECT track.id::text AS entity_id,
				       track.title AS label,
				       COUNT(*)::integer AS relation_count
				FROM track_credit
				JOIN track ON track.id = track_credit.track_id
				WHERE track_credit.artist_id = ?::uuid
				GROUP BY track.id, track.title
			`,
		},
		{
			domain: managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_WORK,
			sql: `
				SELECT work.id::text AS entity_id,
				       ` + WorkSourceTitleSQL("work") + ` AS label,
				       COUNT(*)::integer AS relation_count
				FROM work_credit
				JOIN work ON work.id = work_credit.work_id
				WHERE work_credit.artist_id = ?::uuid
				GROUP BY work.id
			`,
		},
	}

	impacts := make([]artistDeletionImpact, 0)
	for _, query := range queries {
		var rows []artistDeletionImpactRow
		argumentCount := 1
		if query.domain == managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_PROGRAM_EVENT ||
			query.domain == managev1.ArtistRelationDomain_ARTIST_RELATION_DOMAIN_RELEASE {
			argumentCount = 2
		}
		arguments := make(structured.Values, argumentCount)
		for i := range arguments {
			arguments[i] = artistID
		}
		if err := db.WithContext(ctx).Raw(query.sql, arguments...).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			impacts = append(impacts, artistDeletionImpact{
				Domain:        query.domain,
				EntityID:      row.EntityID,
				Label:         row.Label,
				RelationCount: row.RelationCount,
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
