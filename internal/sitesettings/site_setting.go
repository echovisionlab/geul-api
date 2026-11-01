package sitesettings

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const siteSettingAuthorizationID = "1"

// Deps are the collaborators of the Site Settings use cases. Every field
// except SiteOrigin is required; NewService rejects a missing one.
type Deps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// Assets validate, bind and project the File-backed settings.
	Assets Assets
	// References validate the selected homepage Page and Menus.
	References References
	// OG plans the OG regeneration a settings change requires.
	OG    OGInvalidator
	Audit domainaudit.Appender
	// SiteOrigin is the runtime origin the admin settings report; empty
	// reports none.
	SiteOrigin string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Assets": d.Assets == nil,
		"References": d.References == nil, "OG": d.OG == nil, "Audit": d.Audit == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("site settings dependencies are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// Service holds the Site Settings use cases. Connect and MCP adapters
// translate their requests into its commands and queries.
type Service struct {
	db         *gorm.DB
	spiceDB    *auth.SpiceDBClient
	siteOrigin string
	assets     Assets
	references References
	og         OGInvalidator
	mutations  *mutation.Runner
}

// NewService validates deps and returns the Site Settings use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		db: deps.DB, spiceDB: deps.SpiceDB, siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
		assets: deps.Assets, references: deps.References, og: deps.OG, mutations: mutations,
	}, nil
}

// freshEdit is the in-transaction authority of every settings mutation.
func freshEdit() mutation.Access {
	return mutation.FreshAdminCan(policyv1.SiteSetting.Edit(siteSettingAuthorizationID))
}

func changedSiteSettingKeys(before *model.SiteSettings, after *model.SiteSettings, keys []string) []string {
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

// settingsAudit names the Site Settings record of changedKeys, or none when
// nothing changed.
func settingsAudit(changedKeys []string) mutation.Audit {
	if len(changedKeys) == 0 {
		return mutation.Unchanged()
	}
	return mutation.RequestAudit(sharedtelemetry.AuditSiteSettingsUpdated, settingsAuditRecord(changedKeys))
}

// AppendAudit records the inventory-defined Site Settings mutation from a
// transaction owned by a collaborating domain such as Menu or Map Theme.
func AppendAudit(ctx context.Context, tx *gorm.DB, writer domainaudit.Appender, changedKeys []string) error {
	return domainaudit.AppendRequest(ctx, tx, writer, sharedtelemetry.AuditSiteSettingsUpdated, settingsAuditRecord(changedKeys))
}

func settingsAuditRecord(changedKeys []string) domainaudit.Builder {
	auditFields := siteSettingAuditChangedFields(changedKeys)
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewSiteSettingsUpdatedAuditRecord(metadata, auditFields)
	}
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

func loadSettingsRow(tx *gorm.DB) (*model.SiteSettings, error) {
	var settings model.SiteSettings
	if err := tx.First(&settings, "id = 1").Error; err != nil {
		return nil, err
	}
	return &settings, nil
}

func loadSettingsRowForUpdate(tx *gorm.DB) (*model.SiteSettings, error) {
	var settings model.SiteSettings
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&settings, "id = 1").Error; err != nil {
		return nil, err
	}
	return &settings, nil
}
