package sharelinkadapter

import (
	"context"
	"time"

	"gorm.io/gorm"

	legalpublic "github.com/echovisionlab/geul-api/internal/legal/public"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type PublicTargetResolver struct {
	db *gorm.DB
}

func NewPublicTargetResolver(db *gorm.DB) *PublicTargetResolver {
	if db == nil {
		panic("sharelink public target resolver: db is required")
	}
	return &PublicTargetResolver{db: db}
}

func (r *PublicTargetResolver) IsAutomaticPublicHistoryToken(ctx context.Context, token string, entityType managev1.ShareLinkEntityType, entityID string) (bool, error) {
	return legalpublic.IsAutomaticLegalNoticePublicHistoryToken(ctx, r.db, token, entityType, entityID)
}

func (r *PublicTargetResolver) Resolve(ctx context.Context, entityType managev1.ShareLinkEntityType, entityID string, now time.Time) (sharelinkpublic.Target, error) {
	table, legal := publicTargetTable(entityType)
	if table == "" {
		return sharelinkpublic.Target{}, nil
	}
	if legal {
		return r.resolveLegal(ctx, entityType, table, entityID, now)
	}
	var row struct {
		Slug *string `gorm:"column:slug"`
	}
	if err := r.db.WithContext(ctx).Table(table).Select("slug").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return sharelinkpublic.Target{}, nil
		}
		return sharelinkpublic.Target{}, err
	}
	return sharelinkpublic.Target{Slug: row.Slug, Exists: true}, nil
}

func (r *PublicTargetResolver) resolveLegal(ctx context.Context, entityType managev1.ShareLinkEntityType, table, entityID string, now time.Time) (sharelinkpublic.Target, error) {
	var row struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
	}
	if err := r.db.WithContext(ctx).Table(table).Select("status", "effective_from").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return sharelinkpublic.Target{}, nil
		}
		return sharelinkpublic.Target{}, err
	}
	scheduled, active, archived := legalStatuses(entityType)
	if row.Status == active || row.Status == archived {
		return sharelinkpublic.Target{Exists: true, PublicHistory: true}, nil
	}
	if row.Status != scheduled || row.EffectiveFrom == nil || !row.EffectiveFrom.After(now) {
		return sharelinkpublic.Target{}, nil
	}
	return sharelinkpublic.Target{Exists: true}, nil
}

func publicTargetTable(entityType managev1.ShareLinkEntityType) (string, bool) {
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST:
		return "post", false
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE:
		return "page", false
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_WORK:
		return "work", false
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD:
		return "form", false
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY:
		return "privacy_history", true
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
		return "terms_history", true
	default:
		return "", false
	}
}

func legalStatuses(entityType managev1.ShareLinkEntityType) (string, string, string) {
	if entityType == managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS {
		return managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(), managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(), managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String()
	}
	return managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(), managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String()
}
