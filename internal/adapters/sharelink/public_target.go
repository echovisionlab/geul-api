package sharelinkadapter

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	campaignadapter "github.com/echovisionlab/geul-api/internal/adapters/campaign"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
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

// IsAutomaticPublicHistoryToken reports whether token is the preview link a
// Terms or Privacy update notice mailed out for entityID.
func (r *PublicTargetResolver) IsAutomaticPublicHistoryToken(ctx context.Context, token string, entityType sharelink.EntityType, entityID string) (bool, error) {
	var kind string
	switch entityType {
	case sharelink.EntityPrivacy:
		kind = "privacy"
	case sharelink.EntityTerms:
		kind = "terms"
	default:
		return false, nil
	}
	return campaignadapter.LegalNoticeRuns{}.IsUpdateNoticePreviewToken(ctx, r.db, kind, entityID, token)
}

func (r *PublicTargetResolver) Resolve(ctx context.Context, entityType sharelink.EntityType, entityID string, now time.Time) (sharelinkpublic.Target, error) {
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
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sharelinkpublic.Target{}, nil
		}
		return sharelinkpublic.Target{}, err
	}
	return sharelinkpublic.Target{Slug: row.Slug, Exists: true}, nil
}

func (r *PublicTargetResolver) resolveLegal(ctx context.Context, entityType sharelink.EntityType, table, entityID string, now time.Time) (sharelinkpublic.Target, error) {
	var row struct {
		Status        string     `gorm:"column:status"`
		EffectiveFrom *time.Time `gorm:"column:effective_from"`
	}
	if err := r.db.WithContext(ctx).Table(table).Select("status", "effective_from").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
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

func publicTargetTable(entityType sharelink.EntityType) (string, bool) {
	switch entityType {
	case sharelink.EntityPost:
		return "post", false
	case sharelink.EntityPage:
		return "page", false
	case sharelink.EntityWork:
		return "work", false
	case sharelink.EntityRelease:
		return "release", false
	case sharelink.EntityForm, sharelink.EntityFormDashboard:
		return "form", false
	case sharelink.EntityLabel:
		return "label", false
	case sharelink.EntityArtist:
		return "artist", false
	case sharelink.EntityPrivacy:
		return "privacy_history", true
	case sharelink.EntityTerms:
		return "terms_history", true
	default:
		return "", false
	}
}

func legalStatuses(entityType sharelink.EntityType) (string, string, string) {
	if entityType == sharelink.EntityTerms {
		return string(model.TermsStatusScheduled), string(model.TermsStatusActive), string(model.TermsStatusArchived)
	}
	return string(model.PrivacyStatusScheduled), string(model.PrivacyStatusActive), string(model.PrivacyStatusArchived)
}
