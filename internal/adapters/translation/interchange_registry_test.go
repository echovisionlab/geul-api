package translationadapter

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	core "github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

type interchangeRegistryPort struct {
	loadEntityType string
	loadEntityID   string
	loadLocale     string
	applyCommand   application.TranslationInterchangeApply
	loadResult     application.TranslationInterchangeTargetState
	applyResult    application.TranslationInterchangeApplyResult
}

func (p *interchangeRegistryPort) LoadTranslationInterchangeTarget(
	_ context.Context,
	_ *gorm.DB,
	_ *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	_ *core.ExtractionPlan,
) (application.TranslationInterchangeTargetState, error) {
	p.loadEntityType, p.loadEntityID, p.loadLocale = entityType, entityID, locale
	return p.loadResult, nil
}

func (p *interchangeRegistryPort) ApplyTranslationInterchange(
	_ context.Context,
	_ *gorm.DB,
	_ *contentblock.Store,
	command application.TranslationInterchangeApply,
) (application.TranslationInterchangeApplyResult, error) {
	p.applyCommand = command
	return p.applyResult, nil
}

func TestNewInterchangeRegistryRequiresExactlyOnePortPerCatalogDomain(t *testing.T) {
	complete := completeInterchangeRegistrations("", nil)
	if _, err := NewInterchangeRegistry(complete...); err != nil {
		t.Fatalf("NewInterchangeRegistry() error = %v", err)
	}
	if _, err := NewInterchangeRegistry(complete[:len(complete)-1]...); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing registration error = %v", err)
	}
	duplicate := append(append([]InterchangeDomainRegistration(nil), complete...), complete[0])
	if _, err := NewInterchangeRegistry(duplicate...); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate registration error = %v", err)
	}
	if _, err := NewInterchangeRegistry(InterchangeDomainRegistration{Domain: core.Kind("unknown"), Port: &interchangeRegistryPort{}}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported registration error = %v", err)
	}
	var typedNil *interchangeRegistryPort
	if _, err := NewInterchangeRegistry(InterchangeDomainRegistration{Domain: core.KindPost, Port: typedNil}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("typed-nil registration error = %v", err)
	}
}

func TestInterchangeRegistryRoutesValidatedPlanAndApplyToOwningPort(t *testing.T) {
	post := &interchangeRegistryPort{
		loadResult:  application.TranslationInterchangeTargetState{Exists: true, Revision: "tr1_post"},
		applyResult: application.TranslationInterchangeApplyResult{Revision: "tr1_next", Changed: true},
	}
	registry, err := NewInterchangeRegistry(completeInterchangeRegistrations(core.KindPost, post)...)
	if err != nil {
		t.Fatalf("NewInterchangeRegistry() error = %v", err)
	}
	plan := &core.ExtractionPlan{EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en"}
	state, err := registry.LoadTranslationInterchangeTarget(context.Background(), &gorm.DB{}, nil, "post", "post-a", "en", plan)
	if err != nil || state.Revision != "tr1_post" || post.loadEntityType != "post" || post.loadEntityID != "post-a" || post.loadLocale != "en" {
		t.Fatalf("LoadTranslationInterchangeTarget() = (%+v, %v), port = %+v", state, err, post)
	}
	command := application.TranslationInterchangeApply{
		EntityType: "post", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en",
		Plan: plan, Source: &core.SourceDocument{},
	}
	result, err := registry.ApplyTranslationInterchange(context.Background(), &gorm.DB{}, nil, command)
	if err != nil || result.Revision != "tr1_next" || post.applyCommand.EntityID != "post-a" {
		t.Fatalf("ApplyTranslationInterchange() = (%+v, %v), port command = %+v", result, err, post.applyCommand)
	}
}

func TestInterchangeRegistryRejectsMismatchedPlanBeforeDomainCall(t *testing.T) {
	post := &interchangeRegistryPort{}
	registry, err := NewInterchangeRegistry(completeInterchangeRegistrations(core.KindPost, post)...)
	if err != nil {
		t.Fatalf("NewInterchangeRegistry() error = %v", err)
	}
	plan := &core.ExtractionPlan{EntityType: "page", EntityID: "post-a", SourceLocale: "ko", TargetLocale: "en"}
	if _, err := registry.LoadTranslationInterchangeTarget(context.Background(), &gorm.DB{}, nil, "post", "post-a", "en", plan); err == nil {
		t.Fatal("mismatched load plan reached domain port")
	}
	if post.loadEntityType != "" {
		t.Fatalf("mismatched load reached port as %q", post.loadEntityType)
	}
}

func completeInterchangeRegistrations(
	replace core.Kind,
	port application.TranslationInterchangeDomains,
) []InterchangeDomainRegistration {
	definitions := core.Definitions()
	registrations := make([]InterchangeDomainRegistration, 0, len(definitions))
	for _, definition := range definitions {
		selected := application.TranslationInterchangeDomains(&interchangeRegistryPort{})
		if definition.Kind == replace && port != nil {
			selected = port
		}
		registrations = append(registrations, InterchangeDomainRegistration{Domain: definition.Kind, Port: selected})
	}
	return registrations
}
