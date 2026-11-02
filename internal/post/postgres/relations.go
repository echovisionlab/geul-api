package postpostgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// taxonomyTables are the target table, junction table and reference column
// of a taxonomy.
func taxonomyTables(taxonomy post.Taxonomy) (target, junction, column string, err error) {
	switch taxonomy {
	case post.Categories:
		return "category", "post_category", "category_id", nil
	case post.Tags:
		return "tag", "post_tag", "tag_id", nil
	default:
		return "", "", "", fmt.Errorf("unsupported post taxonomy %q", taxonomy)
	}
}

func (s *store) TaxonomyIDs(ctx context.Context, postID string) ([]string, []string, error) {
	var row struct {
		CategoryIDs pq.StringArray `gorm:"column:category_ids;type:text[]"`
		TagIDs      pq.StringArray `gorm:"column:tag_ids;type:text[]"`
	}
	result := s.tx.WithContext(ctx).Raw(
		`SELECT ARRAY(
		          SELECT post_category.category_id::text
		          FROM post_category
		          WHERE post_category.post_id = ?::uuid
		          ORDER BY post_category.category_id
		        ) AS category_ids,
		        ARRAY(
		          SELECT post_tag.tag_id::text
		          FROM post_tag
		          WHERE post_tag.post_id = ?::uuid
		          ORDER BY post_tag.tag_id
		        ) AS tag_ids`,
		postID, postID,
	).Scan(&row)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	return []string(row.CategoryIDs), []string(row.TagIDs), nil
}

func (s *store) CountTaxonomy(ctx context.Context, taxonomy post.Taxonomy, ids []string) (int64, error) {
	target, _, _, err := taxonomyTables(taxonomy)
	if err != nil {
		return 0, err
	}
	var count int64
	err = s.tx.WithContext(ctx).Table(target).Where("id IN ?", ids).Count(&count).Error
	return count, err
}

func (s *store) InsertTaxonomyLinks(ctx context.Context, taxonomy post.Taxonomy, postID string, ids []string) error {
	_, junction, column, err := taxonomyTables(taxonomy)
	if err != nil {
		return err
	}
	for _, id := range ids {
		query := "INSERT INTO " + junction + " (post_id, " + column + ", created_at) VALUES (?, ?, NOW())"
		if err := s.tx.WithContext(ctx).Exec(query, postID, id).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *store) ReplaceTaxonomyLinks(ctx context.Context, taxonomy post.Taxonomy, postID string, ids []string) error {
	_, junction, column, err := taxonomyTables(taxonomy)
	if err != nil {
		return err
	}
	if err := s.tx.WithContext(ctx).Table(junction).
		Where("post_id = ?", postID).
		Delete(nil).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	values := make([]structured.Fields, len(ids))
	for i, id := range ids {
		values[i] = structured.Fields{"post_id": postID, column: id}
	}
	return s.tx.WithContext(ctx).Table(junction).Create(values).Error
}

func (s *store) AuthorLinks(ctx context.Context, postIDs []string) ([]post.AuthorLink, error) {
	var rows []struct {
		PostID   string `gorm:"column:post_id"`
		MemberID string `gorm:"column:member_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("post_author").
		Select("post_id, member_id").
		Where("post_id IN ?", postIDs).
		Order("post_id ASC, created_at ASC, member_id ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	links := make([]post.AuthorLink, len(rows))
	for index, row := range rows {
		links[index] = post.AuthorLink{PostID: row.PostID, MemberID: row.MemberID}
	}
	return links, nil
}

func (s *store) CommentCounts(ctx context.Context, postIDs []string) (map[string]int32, error) {
	var rows []struct {
		PostID string `gorm:"column:post_id"`
		Count  int64  `gorm:"column:comment_count"`
	}
	if err := s.tx.WithContext(ctx).Table("comment").
		Select("post_id, COUNT(*) AS comment_count").
		Where("post_id IN ?", postIDs).
		Group("post_id").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int32, len(postIDs))
	for _, row := range rows {
		result[row.PostID] = int32(row.Count)
	}
	return result, nil
}

func (s *store) ListParticipants(ctx context.Context, postID string) ([]post.ParticipantRow, error) {
	var rows []struct {
		MemberID              string    `gorm:"column:member_id"`
		IdentityID            string    `gorm:"column:identity_id"`
		Role                  string    `gorm:"column:role"`
		CreatedAt             time.Time `gorm:"column:created_at"`
		HasEffectiveAuthority bool      `gorm:"column:has_effective_authority"`
	}
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT author.member_id::text AS member_id,
		       identity.id::text AS identity_id,
		       'author' AS role,
		       author.created_at,
		       CASE
		         WHEN identity.id IS NULL THEN FALSE
		         WHEN member.deleted_at IS NOT NULL OR member.onboarded = FALSE THEN FALSE
		         WHEN identity.state <> 'active' THEN FALSE
		         WHEN COALESCE((identity.metadata_admin ->> 'banned')::boolean, false) THEN FALSE
		         ELSE TRUE
		       END AS has_effective_authority
		FROM post_author AS author
		JOIN member ON member.id = author.member_id
		`+identityread.LeftJoinLinked("member", "identity")+`
		WHERE author.post_id = ?::uuid
		UNION ALL
		SELECT collaborator.member_id::text AS member_id,
		       identity.id::text AS identity_id,
		       'collaborator' AS role,
		       collaborator.created_at,
		       CASE
		         WHEN identity.id IS NULL THEN FALSE
		         WHEN member.deleted_at IS NOT NULL OR member.onboarded = FALSE THEN FALSE
		         WHEN identity.state <> 'active' THEN FALSE
		         WHEN COALESCE((identity.metadata_admin ->> 'banned')::boolean, false) THEN FALSE
		         ELSE TRUE
		       END AS has_effective_authority
		FROM post_collaborator AS collaborator
		JOIN member ON member.id = collaborator.member_id
		`+identityread.LeftJoinLinked("member", "identity")+`
		WHERE collaborator.post_id = ?::uuid
		ORDER BY role ASC, created_at ASC, member_id ASC
	`, postID, postID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	participants := make([]post.ParticipantRow, len(rows))
	for index, row := range rows {
		role := post.RoleCollaborator
		if row.Role == "author" {
			role = post.RoleAuthor
		}
		participants[index] = post.ParticipantRow{
			MemberID: row.MemberID, IdentityID: row.IdentityID, Role: role, CreatedAt: row.CreatedAt,
			HasEffectiveAuthority: row.HasEffectiveAuthority,
		}
	}
	return participants, nil
}

// participantTable is the relation table of role.
func participantTable(role post.ParticipantRole) (string, error) {
	switch role {
	case post.RoleAuthor:
		return "post_author", nil
	case post.RoleCollaborator:
		return "post_collaborator", nil
	default:
		return "", fmt.Errorf("unsupported post participant role %d", role)
	}
}

func (s *store) ParticipantCreatedAt(ctx context.Context, postID, memberID string, role post.ParticipantRole) (time.Time, error) {
	table, err := participantTable(role)
	if err != nil {
		return time.Time{}, err
	}
	var existing struct {
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	err = s.tx.WithContext(ctx).Table(table).
		Select("created_at").
		Where("post_id = ?::uuid AND member_id = ?::uuid", postID, memberID).
		Scan(&existing).Error
	return existing.CreatedAt, err
}

func (s *store) ParticipantExists(ctx context.Context, postID, memberID string, role post.ParticipantRole) (bool, error) {
	table, err := participantTable(role)
	if err != nil {
		return false, err
	}
	var count int64
	if err := s.tx.WithContext(ctx).Table(table).
		Where("post_id = ? AND member_id = ?", postID, memberID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *store) CountAuthors(ctx context.Context, postID, memberID string) (int64, error) {
	query := s.tx.WithContext(ctx).Table("post_author")
	if memberID != "" {
		query = query.Where("post_id = ?::uuid AND member_id = ?::uuid", postID, memberID)
	} else {
		query = query.Where("post_id = ?::uuid", postID)
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}

func (s *store) InsertCreatorAuthor(ctx context.Context, postID, memberID string) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO post_author (post_id, member_id, created_at) VALUES (?::uuid, ?::uuid, NOW())`,
		postID,
		memberID,
	).Error
}

func (s *store) InsertParticipant(ctx context.Context, postID, memberID string, role post.ParticipantRole, createdAt time.Time) error {
	table, err := participantTable(role)
	if err != nil {
		return err
	}
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO `+table+` (post_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, postID, memberID, createdAt).Error
}

func (s *store) DeleteParticipant(ctx context.Context, postID, memberID string, role post.ParticipantRole) (int64, error) {
	table, err := participantTable(role)
	if err != nil {
		return 0, err
	}
	result := s.tx.WithContext(ctx).Exec(
		"DELETE FROM "+table+" WHERE post_id = ?::uuid AND member_id = ?::uuid",
		postID, memberID,
	)
	return result.RowsAffected, result.Error
}

func (s *store) LockMemberIdentity(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLocked(ctx, s.tx, memberID)
	if err != nil {
		return "", err
	}
	return target.IdentityID, nil
}

func (s *store) LockLinkedMemberIdentity(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLockedLinked(ctx, s.tx, memberID)
	if err != nil {
		return "", err
	}
	return target.IdentityID, nil
}

func (s *store) MemberNicknames(ctx context.Context, memberIDs []string) (map[string]string, error) {
	var members []model.Member
	if err := s.tx.WithContext(ctx).Select("id", "nickname").Where("id IN ?", memberIDs).Find(&members).Error; err != nil {
		return nil, err
	}
	names := make(map[string]string, len(members))
	for i := range members {
		if nickname := strings.TrimSpace(members[i].Nickname); nickname != "" {
			names[members[i].ID] = nickname
		}
	}
	return names, nil
}
