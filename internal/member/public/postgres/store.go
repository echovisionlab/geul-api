// Package memberpublicpostgres is the PostgreSQL Store of the public Member reads and
// the token-authenticated newsletter opt-out.
package memberpublicpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"

	"github.com/echovisionlab/geul-api/internal/mediamime"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"

	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

// NewUnits is the production unit of work of the public Member use cases.
// cdnDomain prefixes avatar URLs; empty keeps them relative.
func NewUnits(db *gorm.DB, cdnDomain string) (mutation.UnitOfWork[public.Store], error) {
	if db == nil {
		return nil, errors.New("public member database is required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store { return store{tx: tx, cdnDomain: cdnDomain} }), nil
}

// LoadPublicMemberSummaries reads the public summary of each onboarded
// Member in memberIDs, for the public reads of other domains. Its reads run
// on db directly, as their callers' own reads do.
func LoadPublicMemberSummaries(ctx context.Context, db *gorm.DB, cdnDomain string, memberIDs []string) (map[string]*memberref.Summary, error) {
	summaries, err := public.LoadSummaries(ctx, store{tx: db, cdnDomain: cdnDomain}, memberIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*memberref.Summary, len(summaries))
	for id, summary := range summaries {
		result[id] = &summary
	}
	return result, nil
}

type store struct {
	tx        *gorm.DB
	cdnDomain string
}

var _ public.Store = store{}

type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == memberdomain.ErrMemberNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func (s store) db(ctx context.Context) *gorm.DB { return s.tx.WithContext(ctx) }

func (s store) PublicMember(ctx context.Context, memberID string) (memberdomain.Member, error) {
	var row model.Member
	if err := s.db(ctx).
		Table("member AS m").
		Select("m.*").
		Where("m.id = ? AND m.onboarded = TRUE", memberID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return memberdomain.Member{}, notFoundError{err}
		}
		return memberdomain.Member{}, err
	}
	return memberFromRow(row), nil
}

func (s store) PublicMembers(ctx context.Context, memberIDs []string) ([]memberdomain.Member, error) {
	var rows []model.Member
	if err := s.db(ctx).
		Where("id IN ? AND onboarded = TRUE", memberIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load members: %w", err)
	}
	members := make([]memberdomain.Member, len(rows))
	for i, row := range rows {
		members[i] = memberFromRow(row)
	}
	return members, nil
}

type readyPublicAssetRow struct {
	AssetID     string `gorm:"column:asset_id"`
	Kind        string `gorm:"column:kind"`
	Extension   string `gorm:"column:extension"`
	MimeType    string `gorm:"column:mime_type"`
	FileSize    int64  `gorm:"column:file_size"`
	SHA256      []byte `gorm:"column:sha256"`
	Disposition string `gorm:"column:disposition"`
}

func readyPublicAssetSelect(alias string) string {
	return fmt.Sprintf(`
		%s.id AS asset_id, %s.kind, %s.extension, %s.mime_type,
		%s.file_size, %s.sha256, %s.disposition
	`, alias, alias, alias, alias, alias, alias, alias)
}

func projectReadyPublicAsset(cdnDomain string, row readyPublicAssetRow) (*memberref.Asset, error) {
	if strings.TrimSpace(row.AssetID) == "" || strings.TrimSpace(row.Extension) == "" ||
		strings.TrimSpace(row.MimeType) == "" {
		return nil, errors.New("ready public asset metadata is incomplete")
	}
	if row.FileSize <= 0 || len(row.SHA256) != 32 || row.Disposition != "inline" {
		return nil, errors.New("ready public asset integrity metadata is incomplete")
	}
	if expected := mediamime.GetExtensionFromMime(row.MimeType); expected == "bin" || expected != row.Extension {
		return nil, errors.New("ready public asset extension does not match MIME type")
	}
	assetPath, err := mediaauth.AssetPath(row.AssetID, row.Kind, row.Extension)
	if err != nil {
		return nil, err
	}
	return &memberref.Asset{
		ID:          row.AssetID,
		URL:         strings.TrimRight(cdnDomain, "/") + assetPath,
		Extension:   row.Extension,
		MIMEType:    row.MimeType,
		FileSize:    row.FileSize,
		SHA256:      append([]byte(nil), row.SHA256...),
		Disposition: memberref.AssetDisposition(mediaasset.DispositionInline),
	}, nil
}

func (s store) PublicAvatars(ctx context.Context, memberIDs []string) (map[string]*memberref.Asset, error) {
	ids := sliceutil.UniqueNonZero(memberIDs)
	result := make(map[string]*memberref.Asset, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	type avatarRow struct {
		MemberID string              `gorm:"column:member_id"`
		Asset    readyPublicAssetRow `gorm:"embedded"`
	}
	var avatarRows []avatarRow
	if err := s.db(ctx).
		Table("public_asset_binding AS binding").
		Select("binding.owner_id AS member_id, "+readyPublicAssetSelect("asset")).
		Joins("JOIN public_asset AS asset ON asset.id = binding.asset_id").
		Where("binding.owner_type = ? AND binding.binding_key = ?", "member", "avatar").
		Where("binding.owner_id IN ? AND asset.status = ?", ids, model.PublicAssetStatusReady).
		Find(&avatarRows).Error; err != nil {
		return nil, fmt.Errorf("load member avatars: %w", err)
	}
	for _, row := range avatarRows {
		avatar, err := projectReadyPublicAsset(s.cdnDomain, row.Asset)
		if err != nil {
			return nil, fmt.Errorf("project member avatar %s: %w", row.MemberID, err)
		}
		result[row.MemberID] = avatar
	}
	return result, nil
}

type authorRow struct {
	MemberID          string  `gorm:"column:member_id"`
	AccountIdentityID *string `gorm:"column:account_identity_id"`
	PostCount         int64   `gorm:"column:post_count"`
	Bio               *string `gorm:"column:bio"`
}

func authorsFromRows(rows []authorRow) []public.AuthorRow {
	authors := make([]public.AuthorRow, len(rows))
	for i, row := range rows {
		authors[i] = public.AuthorRow{MemberID: row.MemberID, AccountIdentityID: row.AccountIdentityID, PostCount: row.PostCount, Bio: row.Bio}
	}
	return authors
}

func (s store) TopAuthors(ctx context.Context, limit int32) ([]public.AuthorRow, error) {
	var rows []authorRow
	if err := s.db(ctx).Raw(`
		SELECT
			pa.member_id,
			COUNT(*) AS post_count,
			m.account_identity_id::text AS account_identity_id,
			m.bio
		FROM post_author AS pa
		JOIN post AS p ON p.id = pa.post_id
		JOIN member AS m ON m.id = pa.member_id
		WHERE p.status IN (?, ?)
		  AND m.onboarded = TRUE
		GROUP BY pa.member_id, m.account_identity_id, m.bio
		ORDER BY post_count DESC, pa.member_id ASC
		LIMIT ?
	`, "POST_STATUS_PUBLISHED", "POST_STATUS_ARCHIVED", limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return authorsFromRows(rows), nil
}

func (s store) SelectedAuthors(ctx context.Context, memberIDs []string) ([]public.AuthorRow, error) {
	var rows []authorRow
	if err := s.db(ctx).Raw(`
		SELECT
			m.id::text AS member_id,
			m.account_identity_id::text AS account_identity_id,
			COUNT(pa.post_id) FILTER (WHERE p.status IN (?, ?)) AS post_count,
			m.bio
		FROM member AS m
		LEFT JOIN post_author AS pa ON pa.member_id = m.id
		LEFT JOIN post AS p ON p.id = pa.post_id
		WHERE m.id IN ?
		  AND m.onboarded = TRUE
		  AND m.deleted_at IS NULL
		  AND m.account_identity_id IS NOT NULL
		GROUP BY m.id, m.account_identity_id, m.bio
	`, "POST_STATUS_PUBLISHED", "POST_STATUS_ARCHIVED", memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return authorsFromRows(rows), nil
}

func (s store) NewsletterMemberID(ctx context.Context, identityID string) (string, error) {
	var row struct {
		ID string `gorm:"column:id"`
	}
	err := s.db(ctx).
		Table("member AS m").
		Select("m.id::text AS id").
		Where("m.account_identity_id = ?::uuid AND m.deleted_at IS NULL", identityID).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", notFoundError{err}
	}
	return row.ID, err
}

func (s store) SubscribeNewsletter(ctx context.Context, identityID string, subscribedAt time.Time) (bool, error) {
	row := model.NewsletterSubscription{IdentityID: identityID, SubscribedAt: subscribedAt}
	result := s.db(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	return result.RowsAffected == 1, result.Error
}

func (s store) UnsubscribeNewsletter(ctx context.Context, identityID string) (bool, error) {
	result := s.db(ctx).Where("identity_id = ?", identityID).Delete(&model.NewsletterSubscription{})
	return result.RowsAffected == 1, result.Error
}

func (s store) NewsletterSubscription(ctx context.Context, identityID string) (*time.Time, error) {
	var row model.NewsletterSubscription
	err := s.db(ctx).Where("identity_id = ?", identityID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row.SubscribedAt, nil
}

func memberFromRow(row model.Member) memberdomain.Member {
	return memberdomain.Member{
		ID: row.ID, AccountIdentityID: row.AccountIdentityID, Nickname: row.Nickname, Onboarded: row.Onboarded,
		PrimaryEmail: row.PrimaryEmail, AvailableEmails: []string(row.AvailableEmails),
		Bio: row.Bio, Website: row.Website, SocialLinks: row.SocialLinks, PreferredLocale: row.PreferredLocale,
		DeletedAt: row.DeletedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}
