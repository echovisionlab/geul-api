package sitesettings

import (
	"context"
	"reflect"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const siteSettingAuthorizationID = "1"

type SiteSettingService struct {
	db          *gorm.DB
	spiceDB     *auth.SpiceDBClient
	siteOrigin  string
	auditWriter domainaudit.Appender
	assets      Assets
	references  References
	og          OGInvalidator
}

func NewSiteSettingService(
	db *gorm.DB,
	siteOrigin string,
	assets Assets,
	references References,
	og OGInvalidator,
	spiceDB *auth.SpiceDBClient,
) *SiteSettingService {
	if db == nil || assets == nil || references == nil || og == nil || spiceDB == nil {
		panic("site settings dependencies are required")
	}
	return &SiteSettingService{
		db: db, spiceDB: spiceDB, siteOrigin: strings.TrimRight(strings.TrimSpace(siteOrigin), "/"),
		assets: assets, references: references, og: og,
	}
}

func NewAuditedSiteSettingService(
	db *gorm.DB,
	siteOrigin string,
	assets Assets,
	references References,
	og OGInvalidator,
	auditWriter domainaudit.Appender,
	spiceDB *auth.SpiceDBClient,
) *SiteSettingService {
	if auditWriter == nil {
		panic("site setting audit writer is required")
	}
	service := NewSiteSettingService(db, siteOrigin, assets, references, og, spiceDB)
	service.auditWriter = auditWriter
	return service
}

func (s *SiteSettingService) changedSiteSettingKeys(
	before *model.SiteSettings,
	after *model.SiteSettings,
	keys []string,
) []string {
	seen := make(map[string]struct{}, len(keys))
	changed := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		beforeValue, beforeKnown := settingValue(before, key)
		afterValue, afterKnown := settingValue(after, key)
		if beforeKnown && afterKnown && !reflect.DeepEqual(beforeValue, afterValue) {
			changed = append(changed, key)
		}
	}
	sort.Strings(changed)
	return changed
}

func (s *SiteSettingService) appendSettingsAudit(
	ctx context.Context,
	tx *gorm.DB,
	changedKeys []string,
) error {
	return AppendAudit(ctx, tx, s.auditWriter, changedKeys)
}

// AppendAudit records the inventory-defined Site Settings mutation from a
// transaction owned by a collaborating domain such as Menu or Map Theme.
func AppendAudit(
	ctx context.Context,
	tx *gorm.DB,
	writer domainaudit.Appender,
	changedKeys []string,
) error {
	if writer == nil {
		return nil
	}
	auditFields := siteSettingAuditChangedFields(changedKeys)
	return domainaudit.AppendRequest(
		ctx,
		tx,
		writer,
		sharedtelemetry.AuditSiteSettingsUpdated,
		func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewSiteSettingsUpdatedAuditRecord(metadata, auditFields)
		},
	)
}

// siteSettingAuditChangedFields exposes the inventory-defined aggregate field
// for nested OG section mutations. Storage accepts independently editable
// home/content sections, while the typed audit contract owns only the
// og_image_config aggregate.
func siteSettingAuditChangedFields(changedKeys []string) []string {
	seen := make(map[string]struct{}, len(changedKeys))
	fields := make([]string, 0, len(changedKeys))
	for _, key := range changedKeys {
		if key == "og_image_config.home" || key == "og_image_config.content" {
			key = "og_image_config"
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}

func (s *SiteSettingService) loadSettingsRow(tx *gorm.DB) (*model.SiteSettings, error) {
	var settings model.SiteSettings
	if err := tx.First(&settings, "id = 1").Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

func (s *SiteSettingService) loadSettingsRowForUpdate(tx *gorm.DB) (*model.SiteSettings, error) {
	var settings model.SiteSettings
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&settings, "id = 1").Error; err != nil {
		return nil, err
	}
	return &settings, nil
}
