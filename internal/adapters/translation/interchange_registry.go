package translationadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// InterchangeDomainRegistration binds one Translation target to its owning
// XLIFF target adapter. Persistence and domain behavior stay in the registered
// port; this common registry only validates completeness and routes calls.
type InterchangeDomainRegistration struct {
	Domain core.Kind
	Port   application.TranslationInterchangeDomains
}

// InterchangeRegistry is the complete fail-closed router used by the shared
// Translation application. Each domain can implement and test its port in an
// independent adapter file without adding a central persistence type-switch.
type InterchangeRegistry struct {
	ports map[core.Kind]application.TranslationInterchangeDomains
}

func NewInterchangeRegistry(registrations ...InterchangeDomainRegistration) (*InterchangeRegistry, error) {
	definitions := core.Definitions()
	supported := make(map[core.Kind]struct{}, len(definitions))
	for _, definition := range definitions {
		supported[definition.Kind] = struct{}{}
	}

	ports := make(map[core.Kind]application.TranslationInterchangeDomains, len(definitions))
	for index, registration := range registrations {
		if _, ok := supported[registration.Domain]; !ok {
			return nil, fmt.Errorf("translation interchange registration %d has unsupported domain %q", index, registration.Domain)
		}
		if interchangePortIsNil(registration.Port) {
			return nil, fmt.Errorf("translation interchange domain %q port is required", registration.Domain)
		}
		if _, duplicate := ports[registration.Domain]; duplicate {
			return nil, fmt.Errorf("translation interchange domain %q is registered more than once", registration.Domain)
		}
		ports[registration.Domain] = registration.Port
	}

	missing := make([]string, 0, len(definitions)-len(ports))
	for _, definition := range definitions {
		if _, ok := ports[definition.Kind]; !ok {
			missing = append(missing, string(definition.Kind))
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("translation interchange domain ports are missing: %s", strings.Join(missing, ", "))
	}
	return &InterchangeRegistry{ports: ports}, nil
}

func (r *InterchangeRegistry) LoadTranslationInterchangeTarget(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	plan *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	port, err := r.domainPort(entityType)
	if err != nil {
		return application.TranslationInterchangeTargetState{}, err
	}
	if plan == nil || plan.EntityType != entityType || plan.EntityID != entityID || plan.TargetLocale != locale {
		return application.TranslationInterchangeTargetState{}, fmt.Errorf("translation interchange %q load plan identity does not match the route", entityType)
	}
	return port.LoadTranslationInterchangeTarget(ctx, db, store, entityType, entityID, locale, plan)
}

func (r *InterchangeRegistry) ApplyTranslationInterchange(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	port, err := r.domainPort(command.EntityType)
	if err != nil {
		return application.TranslationInterchangeApplyResult{}, err
	}
	if command.Plan == nil || command.Source == nil ||
		command.Plan.EntityType != command.EntityType ||
		command.Plan.EntityID != command.EntityID ||
		command.Plan.SourceLocale != command.SourceLocale ||
		command.Plan.TargetLocale != command.TargetLocale {
		return application.TranslationInterchangeApplyResult{}, fmt.Errorf(
			"translation interchange %q apply identity does not match the validated plan",
			command.EntityType,
		)
	}
	return port.ApplyTranslationInterchange(ctx, db, store, command)
}

func (r *InterchangeRegistry) domainPort(entityType string) (application.TranslationInterchangeDomains, error) {
	if r == nil {
		return nil, errors.New("translation interchange registry is required")
	}
	definition, ok := core.DefinitionForKind(entityType)
	if !ok {
		return nil, fmt.Errorf("unsupported translation interchange domain %q", entityType)
	}
	port, ok := r.ports[definition.Kind]
	if !ok || interchangePortIsNil(port) {
		return nil, fmt.Errorf("translation interchange domain %q is not registered", entityType)
	}
	return port, nil
}

func interchangePortIsNil(port application.TranslationInterchangeDomains) bool {
	if port == nil {
		return true
	}
	value := reflect.ValueOf(port)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

var _ application.TranslationInterchangeDomains = (*InterchangeRegistry)(nil)
