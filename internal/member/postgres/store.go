// Package memberpostgres is the PostgreSQL Store of the Member domain: the SQL,
// row locks and constraint mapping of the member, user_tag,
// user_tag_mapping, newsletter_subscription and user_cookie_consent rows,
// and the bindings of the other domains a Member transaction reaches.
package memberpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identityread"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// WelcomeEmails enqueues the welcome command in the onboarding transaction.
type WelcomeEmails interface {
	PublishSendEmailWithExecutor(ctx context.Context, executor eventpkg.DBTX, job *managev1.SendEmailEvent) error
}

// WelcomeRecipients resolves the welcome recipient of an Identity through
// the Account email projection, in the onboarding transaction
// (memberadapter.AccountEmailProjection).
type WelcomeRecipients interface {
	ResolveDelivery(ctx context.Context, tx *gorm.DB, identityID string) (recipient, linkedMemberID, reason string, err error)
}

// Ports are the other domains a Member transaction reaches. Every field is
// required.
type Ports struct {
	WelcomeEmails     WelcomeEmails
	WelcomeRecipients WelcomeRecipients
	// CDNDomain prefixes avatar URLs; empty keeps them relative.
	CDNDomain string
}

func (p Ports) validate() error {
	var missing []string
	if p.WelcomeEmails == nil {
		missing = append(missing, "WelcomeEmails")
	}
	if p.WelcomeRecipients == nil {
		missing = append(missing, "WelcomeRecipients")
	}
	if len(missing) != 0 {
		return errors.New("member store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits is the production unit of work of the Member use cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[member.Store], error) {
	if db == nil {
		return nil, errors.New("member database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) member.Store { return store{tx: tx, ports: ports} }), nil
}

// NewStore binds the Member store to a transaction or connection another domain
// opened, for the Member steps Account runs in it (deletion, the email
// projection, the newsletter subscription). Those steps never reach the
// welcome email ports.
func NewStore(tx *gorm.DB) member.Store {
	return store{tx: tx}
}

// NewStoreInTransaction binds the Member store to the caller's transaction and
// refuses a pool, so a terminal Member step cannot commit part of its
// transition on its own.
func NewStoreInTransaction(tx *gorm.DB) (member.Store, error) {
	if tx == nil || tx.Statement == nil {
		return nil, errors.New("member deletion requires the caller's transaction")
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, errors.New("member deletion must run inside the caller's transaction")
	}
	return store{tx: tx}, nil
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

var _ member.Store = store{}

// notFoundError keeps the gorm text of a missing row while answering the
// domain sentinel.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == member.ErrMemberNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func memberNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{err}
	}
	return err
}

type tagNotFoundError struct{ error }

func (err tagNotFoundError) Is(target error) bool { return target == member.ErrTagNotFound }
func (err tagNotFoundError) Unwrap() error        { return err.error }

type uniqueError struct {
	error
	sentinel error
}

func (err uniqueError) Is(target error) bool { return target == err.sentinel }
func (err uniqueError) Unwrap() error        { return err.error }

func uniqueViolation(err, sentinel error) error {
	if dberrors.IsUniqueViolation(err) {
		return uniqueError{error: err, sentinel: sentinel}
	}
	return err
}

func (s store) db(ctx context.Context) *gorm.DB { return s.tx.WithContext(ctx) }

func (s store) Members(ctx context.Context, ids []string) ([]member.Member, error) {
	query := s.db(ctx).Model(&model.Member{})
	if len(ids) != 0 {
		query = query.Where("id IN ?", ids)
	}
	var rows []model.Member
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return membersFromRows(rows), nil
}

func (s store) MemberByID(ctx context.Context, memberID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Where("id = ?::uuid", memberID).Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) SessionMember(ctx context.Context, memberID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).
		Select("id", "account_identity_id", "nickname", "onboarded", "preferred_locale", "deleted_at").
		Where("id = ?", memberID).
		Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) OwnMember(ctx context.Context, memberID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE", memberID).Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) SettingsMember(ctx context.Context, memberID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Where("id = ? AND deleted_at IS NULL AND account_identity_id IS NOT NULL", memberID).First(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) LinkedMember(ctx context.Context, memberID, identityID string) (member.Member, error) {
	var row model.Member
	if err := s.db(ctx).Where("id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, identityID).Take(&row).Error; err != nil {
		return member.Member{}, memberNotFound(err)
	}
	return memberFromRow(row), nil
}

func (s store) SearchMembers(ctx context.Context, search member.MemberSearch) ([]member.Member, error) {
	query := s.db(ctx).Where("deleted_at IS NULL AND account_identity_id IS NOT NULL AND onboarded = TRUE")
	if search.IdentityIDs != nil {
		query = query.Where("account_identity_id IN ?", search.IdentityIDs)
	}
	if search.Query != "" {
		query = query.Where("nickname ILIKE ?", "%"+search.Query+"%")
	}
	if len(search.ExcludeMemberIDs) != 0 {
		query = query.Where("id NOT IN ?", search.ExcludeMemberIDs)
	}
	var rows []model.Member
	if err := query.Order("nickname ASC, id ASC").Limit(search.Limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return membersFromRows(rows), nil
}

func (s store) AdminMemberPage(ctx context.Context, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset int) ([]member.Member, int64, error) {
	list := s.db(ctx).
		Table("member AS m").
		Joins(identityread.LeftJoinLinked("m", "i")).
		Joins("LEFT JOIN newsletter_subscription AS ns ON ns.identity_id = i.id")
	list, err := memberAdminFilterConfig.ApplyFilters(list, filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list, err = memberAdminSortConfig.ApplySort(list, sorts)
	if err != nil {
		return nil, 0, err
	}
	if len(sorts) != 0 {
		list = list.Order("m.id DESC")
	}
	var rows []model.Member
	if err := list.Select("m.*").Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	return membersFromRows(rows), total, nil
}

func (s store) Avatars(ctx context.Context, memberIDs []string) (map[string]*member.Asset, error) {
	result := make(map[string]*member.Asset, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	type avatarRow struct {
		MemberID string `gorm:"column:member_id"`
		model.PublicAsset
	}
	var rows []avatarRow
	if err := s.db(ctx).Raw(`
		SELECT b.owner_id AS member_id, a.*
		FROM public_asset_binding AS b
		JOIN public_asset AS a ON a.id = b.asset_id
		WHERE b.owner_type = 'member'
		  AND b.binding_key = 'avatar'
		  AND b.owner_id IN ?
		  AND a.status = 'ready'
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	lifecycle := mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain)
	for _, row := range rows {
		asset, err := mediaasset.ReferenceResultProto(lifecycle.AssetRef(mediaassetpostgres.Asset(row.PublicAsset)))
		if err != nil {
			return nil, err
		}
		result[row.MemberID] = member.AssetFromMessage(asset)
	}
	return result, nil
}

func (s store) NewsletterSubscriptions(ctx context.Context, memberIDs []string) (map[string]time.Time, error) {
	type subscriptionRow struct {
		MemberID     string    `gorm:"column:member_id"`
		SubscribedAt time.Time `gorm:"column:subscribed_at"`
	}
	var rows []subscriptionRow
	if err := s.db(ctx).Raw(`
		SELECT m.id::text AS member_id, ns.subscribed_at
		FROM member AS m
		`+identityread.JoinLinked("m", "i")+`
		JOIN newsletter_subscription AS ns
		  ON ns.identity_id = i.id
		WHERE m.id IN ?
	`, memberIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]time.Time, len(memberIDs))
	for _, row := range rows {
		result[row.MemberID] = row.SubscribedAt
	}
	return result, nil
}

func (s store) LatestCookieConsent(ctx context.Context, memberID string) (*member.CookieConsent, error) {
	var row model.UserCookieConsent
	err := s.db(ctx).Where("member_id = ?", memberID).Order("recorded_at DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &member.CookieConsent{
		ID: row.ID, MemberID: row.MemberID, Essential: row.Essential, Analytics: row.Analytics,
		Version: row.ConsentVersion, Source: row.Source, RecordedAt: row.RecordedAt,
	}, nil
}

func (s store) NicknameClaimed(ctx context.Context, nickname, exceptMemberID string) (bool, error) {
	var claimed bool
	err := s.db(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM member
			WHERE nickname = ?
			  AND id <> ?::uuid
			  AND deleted_at IS NULL
		)
	`, nickname, exceptMemberID).Scan(&claimed).Error
	return claimed, err
}

func targetFromHelper(target authorizationtarget.Target) member.Target {
	return member.Target{MemberID: target.MemberID, IdentityID: target.IdentityID, Onboarded: target.Onboarded}
}

func targetAnswer(target authorizationtarget.Target, err error) (member.Target, error) {
	if errors.Is(err, authorizationtarget.ErrIneligible) {
		return member.Target{}, member.ErrIneligible
	}
	if err != nil {
		return member.Target{}, err
	}
	return targetFromHelper(target), nil
}

func (s store) RequireTarget(ctx context.Context, memberID string) (member.Target, error) {
	target, err := authorizationtarget.Require(ctx, s.tx, memberID)
	if err != nil {
		return member.Target{}, err
	}
	return targetFromHelper(target), nil
}

func (s store) RequireLinkedTarget(ctx context.Context, memberID string, requireOnboarded bool) (member.Target, error) {
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.tx, memberID, requireOnboarded)
	if err != nil {
		return member.Target{}, err
	}
	return targetFromHelper(target), nil
}

func (s store) CurrentTarget(ctx context.Context, memberID string) (member.Target, error) {
	return targetAnswer(authorizationtarget.ForMember(s.db(ctx), memberID))
}

func (s store) CurrentLinkedTarget(ctx context.Context, memberID string, requireOnboarded bool) (member.Target, error) {
	return targetAnswer(authorizationtarget.LinkedMemberForMember(s.db(ctx), memberID, requireOnboarded))
}

func (s store) UpdateProfile(ctx context.Context, memberID string, update member.ProfileUpdate) error {
	updates := structured.Fields{}
	if update.Nickname != nil {
		updates["nickname"] = *update.Nickname
	}
	if update.Bio.Set {
		updates["bio"] = nullableValue(update.Bio.Value)
	}
	if update.Website.Set {
		updates["website"] = nullableValue(update.Website.Value)
	}
	if update.SocialLinks != nil {
		updates["social_links"] = update.SocialLinks
	}
	updates["updated_at"] = update.UpdatedAt
	err := s.db(ctx).Model(&model.Member{}).Where("id = ?::uuid", memberID).Updates(updates).Error
	return uniqueViolation(err, member.ErrNicknameTaken)
}

func nullableValue(value *string) structured.Value {
	if value == nil {
		return nil
	}
	return *value
}

func (s store) CompleteOnboarding(ctx context.Context, memberID, identityID, nickname string, now time.Time) (bool, error) {
	result := s.db(ctx).Model(&model.Member{}).
		Where(`id = ?::uuid
			AND account_identity_id = ?::uuid
			AND deleted_at IS NULL
			AND onboarded = FALSE`, memberID, identityID).
		Updates(structured.Fields{
			"nickname":   nickname,
			"onboarded":  true,
			"updated_at": now,
		})
	if result.Error != nil {
		return false, uniqueViolation(result.Error, member.ErrNicknameTaken)
	}
	return result.RowsAffected == 1, nil
}

func (s store) UpdatePreferredLocale(ctx context.Context, memberID, locale string, now time.Time) error {
	return s.db(ctx).Model(&model.Member{}).Where("id = ?", memberID).Updates(structured.Fields{
		"preferred_locale": locale,
		"updated_at":       now,
	}).Error
}

func (s store) InsertCookieConsent(ctx context.Context, consent member.CookieConsent) (string, error) {
	row := model.UserCookieConsent{
		MemberID: consent.MemberID, Essential: consent.Essential, Analytics: consent.Analytics,
		ConsentVersion: consent.Version, Source: consent.Source, RecordedAt: consent.RecordedAt,
	}
	if err := s.db(ctx).Create(&row).Error; err != nil {
		return "", err
	}
	return row.ID, nil
}

func (s store) TagIDs(ctx context.Context, memberIDs []string) (map[string][]string, error) {
	var rows []model.UserTagMapping
	if err := s.db(ctx).Where("member_id IN ?", memberIDs).Order("member_id, tag_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string][]string, len(memberIDs))
	for _, row := range rows {
		result[row.MemberID] = append(result[row.MemberID], row.TagID)
	}
	return result, nil
}

func (s store) TagPage(ctx context.Context, filters []*commonv1.FilterSpec, sorts []*commonv1.SortSpec, limit, offset int) ([]member.Tag, int64, error) {
	list := s.db(ctx).Table("user_tag AS t")
	list, err := memberTagAdminFilterConfig.ApplyFilters(list, filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	type row struct {
		model.UserTag
		MemberCount int32 `gorm:"column:member_count"`
	}
	var rows []row
	list = list.Select("t.*, COUNT(m.member_id)::int AS member_count").
		Joins("LEFT JOIN user_tag_mapping AS m ON m.tag_id = t.id").
		Group("t.id")
	list, err = memberTagAdminSortConfig.ApplySort(list, sorts)
	if err != nil {
		return nil, 0, err
	}
	if len(sorts) != 0 {
		list = list.Order("t.id ASC")
	}
	if err := list.Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return nil, 0, err
	}
	tags := make([]member.Tag, len(rows))
	for i, row := range rows {
		tags[i] = member.Tag{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt, MemberCount: row.MemberCount}
	}
	return tags, total, nil
}

func (s store) InsertTag(ctx context.Context, name string) (member.Tag, error) {
	tag := model.UserTag{Name: name}
	if err := s.db(ctx).Create(&tag).Error; err != nil {
		return member.Tag{}, uniqueViolation(err, member.ErrTagNameTaken)
	}
	return member.Tag{ID: tag.ID, Name: tag.Name, CreatedAt: tag.CreatedAt}, nil
}

func (s store) LockTag(ctx context.Context, tagID string) (member.Tag, error) {
	var tag model.UserTag
	if err := s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&tag, "id = ?", tagID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return member.Tag{}, tagNotFoundError{err}
		}
		return member.Tag{}, err
	}
	return member.Tag{ID: tag.ID, Name: tag.Name, CreatedAt: tag.CreatedAt}, nil
}

func (s store) TagReferencedBySegment(ctx context.Context, tagID string) (bool, error) {
	var referenced bool
	err := s.db(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM audience_segment_user_tag
			WHERE user_tag_id = ?
		)
	`, tagID).Scan(&referenced).Error
	return referenced, err
}

func (s store) DeleteTag(ctx context.Context, tagID string) error {
	if err := s.db(ctx).Where("tag_id = ?", tagID).Delete(&model.UserTagMapping{}).Error; err != nil {
		return err
	}
	return s.db(ctx).Delete(&model.UserTag{ID: tagID}).Error
}

func (s store) CountTags(ctx context.Context, tagIDs []string) (int64, error) {
	var count int64
	err := s.db(ctx).Model(&model.UserTag{}).Where("id IN ?", tagIDs).Count(&count).Error
	return count, err
}

func (s store) ReplaceMemberTags(ctx context.Context, memberID string, tagIDs []string) error {
	if err := s.db(ctx).Where("member_id = ?", memberID).Delete(&model.UserTagMapping{}).Error; err != nil {
		return err
	}
	for _, tagID := range tagIDs {
		if err := s.db(ctx).Create(&model.UserTagMapping{MemberID: memberID, TagID: tagID}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s store) AvatarSourceOwned(ctx context.Context, fileID, ownerID string) (bool, error) {
	query := s.db(ctx).Model(&model.FileIngestBinding{}).Where("file_id = ? AND upload_type = ?", fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String())
	if ownerID != "" {
		query = query.Where("entity_id = ?", ownerID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s store) LockAvatarBinding(ctx context.Context, memberID string) (string, bool, error) {
	binding, found, err := mediaassetpostgres.NewStore(s.tx).LockBinding(ctx, "member", memberID, "avatar")
	return binding.AssetID, found, err
}

func (s store) BindAvatar(ctx context.Context, memberID, assetID, sourceFileID string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain).BindPublicAsset(ctx, mediaasset.Binding{AssetID: assetID, OwnerType: "member", OwnerID: memberID, BindingKey: "avatar", SourceFileID: &sourceFileID})
}

func (s store) ReleaseAvatar(ctx context.Context, memberID string) error {
	return mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(s.tx), s.ports.CDNDomain).ReleaseExactPublicAssetBindings(ctx, "member", memberID, []string{"avatar"})
}

func (s store) WelcomeDelivery(ctx context.Context, identityID string) (member.WelcomeDelivery, error) {
	recipient, linkedMemberID, reason, err := s.ports.WelcomeRecipients.ResolveDelivery(ctx, s.tx, identityID)
	return member.WelcomeDelivery{Recipient: recipient, LinkedMemberID: linkedMemberID, Reason: reason}, err
}

func (s store) EnqueueWelcomeEmail(ctx context.Context, welcome member.WelcomeEmail) error {
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	commandID := welcome.CommandID
	job := &managev1.SendEmailEvent{
		Recipient:    welcome.Recipient,
		TemplateType: email.EventWelcome.String(),
		TemplateData: map[string]string{
			"name":      welcome.Nickname,
			"login_url": welcome.LoginURL,
		},
		RecipientContext: email.AccountSelectedPrimaryEmailContext(welcome.IdentityID),
		MessageId:        &commandID,
		Locale:           welcome.Locale,
	}
	return s.ports.WelcomeEmails.PublishSendEmailWithExecutor(ctx, executor, job)
}

func (s store) LockRegistrationIdentity(ctx context.Context, identityID string) error {
	if err := s.db(ctx).Exec(
		`SELECT pg_advisory_xact_lock(hashtextextended('member-provision:' || ?::text, 0))`,
		identityID,
	).Error; err != nil {
		return err
	}
	var collision bool
	if err := s.db(ctx).Raw(
		`SELECT EXISTS (SELECT 1 FROM member WHERE id = ?::uuid)`, identityID,
	).Scan(&collision).Error; err != nil {
		return err
	}
	if collision {
		return fmt.Errorf("identity_id collides with an existing member_id")
	}
	return nil
}

func (s store) LockRegistrationMember(ctx context.Context, identityID string) (member.Member, bool, error) {
	var row model.Member
	err := s.db(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("account_identity_id = ?", identityID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return member.Member{}, false, nil
	}
	if err != nil {
		return member.Member{}, false, err
	}
	return memberFromRow(row), true, nil
}

func (s store) MemberIDOccupied(ctx context.Context, candidate string) (bool, error) {
	var occupied bool
	err := s.db(ctx).Raw(`
		SELECT EXISTS (SELECT 1 FROM member WHERE id = ?::uuid)
		    OR EXISTS (SELECT 1 FROM member WHERE nickname = ?)
		    OR `+identityread.ExistsSQL+`
	`, candidate, candidate, candidate).Scan(&occupied).Error
	return occupied, err
}

func (s store) CreateRegistrationMember(ctx context.Context, created member.Member) (member.Member, error) {
	// account_identity is the durable application anchor and the sole
	// SpiceDB subject. Kratos identity creation and this application anchor are
	// separate lifecycle steps, so establish the anchor in the same
	// transaction as the Member link before the FK is written.
	if err := s.db(ctx).Exec(
		`INSERT INTO public.account_identity (id) VALUES (?::uuid) ON CONFLICT (id) DO NOTHING`,
		*created.AccountIdentityID,
	).Error; err != nil {
		return member.Member{}, fmt.Errorf("create account identity anchor: %w", err)
	}
	row := rowFromMember(created)
	if err := s.db(ctx).Create(&row).Error; err != nil {
		return member.Member{}, err
	}
	return memberFromRow(row), nil
}

func (s store) EmailReuseHeld(ctx context.Context, normalizedEmail string) (bool, error) {
	return member.RegistrationEmailReuseBlocked(ctx, s.tx, normalizedEmail)
}

func (s store) LinkedMemberExists(ctx context.Context, memberID, identityID string) (bool, error) {
	var linked bool
	err := s.db(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1 FROM member
			WHERE id = ?::uuid
			  AND account_identity_id = ?::uuid
			  AND deleted_at IS NULL
		)
	`, memberID, identityID).Scan(&linked).Error
	return linked, err
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

func (s store) PrimaryEmail(ctx context.Context, memberID, identityID string) (string, error) {
	var row struct {
		Email string `gorm:"column:primary_email"`
	}
	result := s.db(ctx).
		Table("member AS member").
		Joins(identityread.JoinLinked("member", "identity")).
		Select("member.primary_email").
		Where("member.id = ?::uuid AND identity.id = ?::uuid AND member.deleted_at IS NULL AND member.primary_email IS NOT NULL", memberID, identityID).
		Take(&row)
	if result.Error != nil {
		return "", memberNotFound(result.Error)
	}
	if result.RowsAffected != 1 {
		return "", notFoundError{gorm.ErrRecordNotFound}
	}
	return row.Email, nil
}

func (s store) LockLinkedMember(ctx context.Context, memberID, identityID string) (string, error) {
	var row struct {
		ID string `gorm:"column:id"`
	}
	result := s.db(ctx).
		Table("member AS member").
		Joins(identityread.JoinLinked("member", "identity")).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "member"}}).
		Select("member.id").
		Where("member.id = ?::uuid AND identity.id = ?::uuid AND member.deleted_at IS NULL", memberID, identityID).
		Take(&row)
	if result.Error != nil {
		return "", memberNotFound(result.Error)
	}
	return row.ID, nil
}

func (s store) UpdateEmailProjection(ctx context.Context, memberID, identityID, primaryEmail string, availableEmails []string) error {
	return s.db(ctx).
		Model(&model.Member{}).
		Where("id = ?::uuid AND account_identity_id = ?::uuid AND deleted_at IS NULL", memberID, identityID).
		Updates(structured.Fields{
			"primary_email":    primaryEmail,
			"available_emails": pq.StringArray(availableEmails),
			"updated_at":       gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error
}
