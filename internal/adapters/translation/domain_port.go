package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	"gorm.io/gorm"
)

type domainRegistration struct {
	domain core.Kind
	port   domainPortFunctions
}

// domainPortFunctions is the owning-domain side of provider translation,
// source-locale mutation, XLIFF authorization, and management projection. The
// common registry routes by the stable Translation catalog; it does not select
// domain SQL, lifecycle, or SpiceDB keys.
type domainPortFunctions struct {
	loadSourceDocument        func(context.Context, *gorm.DB, *contentblock.Store, string) (*core.SourceDocument, error)
	buildExtractionPlan       func(*model.TranslationJob, *core.SourceDocument) (*core.ExtractionPlan, error)
	buildCandidate            func(*core.ExtractionPlan, *core.SourceDocument, map[string]core.UnitResult) (*core.Candidate, error)
	applyCandidate            func(context.Context, *gorm.DB, *contentblock.Store, *model.TranslationJob, *core.Candidate, core.EntryWrite) error
	requestLocaleOG           func(context.Context, *gorm.DB, *og.Planner, *og.Refresher, string, string, string) (bool, error)
	translationEntrySelectSQL func(string) string
	requireEditable           func(context.Context, *gorm.DB, string) error
	requireInterchangeView    func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	requireInterchangeEdit    func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	requireJobRead            func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	requireSourceLocaleEdit   func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	prepareSourceLocale       func(context.Context, *gorm.DB, string, string, string, time.Time) error
	appendSourceLocaleAudit   func(context.Context, *gorm.DB, string, string, string) error
	requireRegeneration       func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error
	requireSourceMutable      func(context.Context, *gorm.DB, string) error
}

func newConfiguredDomainPort(domain core.Kind, functions domainPortFunctions) domainRegistration {
	if functions.loadSourceDocument == nil || functions.buildExtractionPlan == nil ||
		functions.buildCandidate == nil || functions.applyCandidate == nil ||
		functions.translationEntrySelectSQL == nil || functions.requireInterchangeView == nil ||
		functions.requireSourceLocaleEdit == nil || functions.requireJobRead == nil ||
		functions.appendSourceLocaleAudit == nil {
		panic(fmt.Sprintf("translation domain %q port is incomplete", domain))
	}
	if functions.requestLocaleOG == nil {
		functions.requestLocaleOG = noLocaleOG
	}
	if functions.requireEditable == nil {
		functions.requireEditable = noDomainRequirement
	}
	if functions.requireInterchangeEdit == nil {
		functions.requireInterchangeEdit = functions.requireSourceLocaleEdit
	}
	if functions.prepareSourceLocale == nil {
		functions.prepareSourceLocale = prepareScalarSourceLocale(domain)
	}
	if functions.requireRegeneration == nil {
		functions.requireRegeneration = functions.requireSourceLocaleEdit
	}
	if functions.requireSourceMutable == nil {
		functions.requireSourceMutable = noDomainRequirement
	}
	return domainRegistration{domain: domain, port: functions}
}

func buildDomainPorts(registrations []domainRegistration) (map[core.Kind]domainPortFunctions, error) {
	definitions := core.Definitions()
	supported := make(map[core.Kind]struct{}, len(definitions))
	for _, definition := range definitions {
		supported[definition.Kind] = struct{}{}
	}
	ports := make(map[core.Kind]domainPortFunctions, len(definitions))
	for index, registration := range registrations {
		if _, ok := supported[registration.domain]; !ok {
			return nil, fmt.Errorf("translation domain registration %d has unsupported domain %q", index, registration.domain)
		}
		if registration.port.loadSourceDocument == nil {
			return nil, fmt.Errorf("translation domain %q port is required", registration.domain)
		}
		if _, duplicate := ports[registration.domain]; duplicate {
			return nil, fmt.Errorf("translation domain %q is registered more than once", registration.domain)
		}
		ports[registration.domain] = registration.port
	}
	missing := make([]string, 0, len(definitions)-len(ports))
	for _, definition := range definitions {
		if _, ok := ports[definition.Kind]; !ok {
			missing = append(missing, string(definition.Kind))
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("translation domain ports are missing: %s", strings.Join(missing, ", "))
	}
	return ports, nil
}

func (r *DomainRegistry) domainPort(entityType string) (domainPortFunctions, error) {
	if r == nil {
		return domainPortFunctions{}, errors.New("translation domain registry is required")
	}
	definition, ok := core.DefinitionForKind(entityType)
	if !ok {
		return domainPortFunctions{}, fmt.Errorf("unsupported translation entity type %q", entityType)
	}
	port, ok := r.ports[definition.Kind]
	if !ok {
		return domainPortFunctions{}, fmt.Errorf("translation domain %q is not registered", entityType)
	}
	return port, nil
}

func noLocaleOG(context.Context, *gorm.DB, *og.Planner, *og.Refresher, string, string, string) (bool, error) {
	return false, nil
}

func noDomainRequirement(context.Context, *gorm.DB, string) error { return nil }

var _ application.DomainRegistry = (*DomainRegistry)(nil)
