package sitesettings

import (
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const siteSettingAuthorizationID = "1"

// Deps are the collaborators of the Site Settings use cases. Every field
// except SiteOrigin is required; NewService rejects a missing one.
type Deps struct {
	Units   mutation.UnitOfWork[Store] // sitesettingspostgres.NewUnits(db, ports)
	SpiceDB mutation.Authorizer
	Fence   identitystate.Fence // identitystate.PostgresFence{}
	Audit   domainaudit.Appender
	// SiteOrigin is the runtime origin the admin settings report; empty
	// reports none.
	SiteOrigin string
}

func (d Deps) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Audit": d.Audit == nil,
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
	units      mutation.UnitOfWork[Store]
	spiceDB    mutation.Authorizer
	siteOrigin string
	mutations  *mutation.StoreRunner[Store]
}

// NewService validates deps and returns the Site Settings use cases.
func NewService(deps Deps) (*Service, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &Service{
		units: deps.Units, spiceDB: deps.SpiceDB, siteOrigin: strings.TrimRight(strings.TrimSpace(deps.SiteOrigin), "/"),
		mutations: mutations,
	}, nil
}

// freshEdit is the in-transaction authority of every settings mutation.
func freshEdit() mutation.Access {
	return mutation.FreshAdminCan(policyv1.SiteSetting.Edit(siteSettingAuthorizationID))
}

func changedSiteSettingKeys(before, after *Settings, keys []string) []string {
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
	return mutation.RequestAudit(sharedtelemetry.AuditSiteSettingsUpdated, AuditRecord(changedKeys))
}

// AuditRecord builds the AuditSiteSettingsUpdated record of changedKeys,
// for a collaborating domain such as Menu that changes the settings in its
// own transaction.
func AuditRecord(changedKeys []string) domainaudit.Builder {
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
