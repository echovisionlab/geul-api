package aidocumentadapter

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

type registryPort struct {
	loadIdentity    core.DocumentIdentity
	loadLocale      core.Locale
	validateRequest core.ApplyRequest
	executeRequest  core.ApplyRequest
	document        core.Document
	validation      core.ValidationResult
	execution       core.ApplyResult
	loadErr         error
	validateErr     error
	executeErr      error
}

func (p *registryPort) ValidateMutation(
	_ context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	p.validateRequest = request
	return p.validation, p.validateErr
}

func (p *registryPort) ExecuteMutation(
	_ context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	p.executeRequest = request
	return p.execution, p.executeErr
}

func (p *registryPort) Load(
	_ context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	p.loadIdentity = identity
	p.loadLocale = locale
	return p.document, p.loadErr
}

func completeRegistryRegistrations(replaceDomain core.Domain, replacePort core.DomainPort) []DomainRegistration {
	domains := core.SupportedDomains()
	registrations := make([]DomainRegistration, 0, len(domains))
	for _, domain := range domains {
		port := core.DomainPort(&registryPort{})
		if domain == replaceDomain && replacePort != nil {
			port = replacePort
		}
		registrations = append(registrations, DomainRegistration{Domain: domain, Port: port})
	}
	return registrations
}

func TestNewRegistryRejectsIncompleteDuplicateUnsupportedAndNilRegistrations(t *testing.T) {
	domains := core.SupportedDomains()
	tests := []struct {
		name          string
		registrations []DomainRegistration
		wantError     string
	}{
		{name: "missing", registrations: completeRegistryRegistrations(core.DomainPostSeries, nil)[:len(domains)-1], wantError: "post_series"},
		{name: "duplicate", registrations: append(completeRegistryRegistrations("", nil), DomainRegistration{Domain: core.DomainPost, Port: &registryPort{}}), wantError: "more than once"},
		{name: "unsupported", registrations: []DomainRegistration{{Domain: core.Domain("unknown"), Port: &registryPort{}}}, wantError: "unsupported domain"},
		{name: "nil", registrations: []DomainRegistration{{Domain: core.DomainPost}}, wantError: "port is required"},
	}

	var typedNil *registryPort
	tests = append(tests, struct {
		name          string
		registrations []DomainRegistration
		wantError     string
	}{name: "typed nil", registrations: []DomainRegistration{{Domain: core.DomainPost, Port: typedNil}}, wantError: "port is required"})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, err := NewRegistry(test.registrations...)
			if err == nil || registry != nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("NewRegistry() = (%v, %v), want error containing %q", registry, err, test.wantError)
			}
		})
	}
}

func TestRegistryRoutesLoadAndMutationToExactDomain(t *testing.T) {
	domains := core.SupportedDomains()
	if len(domains) != 12 {
		t.Fatalf("registry domain count = %d, want 12", len(domains))
	}
	for _, domain := range domains {
		t.Run(string(domain), func(t *testing.T) {
			selected := &registryPort{
				document: core.Document{
					Identity: core.DocumentIdentity{Domain: domain, Reference: "document-handle"},
					Locale:   "en",
				},
				execution: core.ApplyResult{DocumentRevision: "revision-2"},
			}
			registrations := completeRegistryRegistrations(domain, selected)
			registry, err := NewRegistry(registrations...)
			if err != nil {
				t.Fatal(err)
			}

			identity := core.DocumentIdentity{Domain: domain, Reference: "document-handle"}
			document, err := registry.Load(context.Background(), identity, "en")
			if err != nil {
				t.Fatal(err)
			}
			if document.Identity != identity || selected.loadIdentity != identity || selected.loadLocale != "en" {
				t.Fatalf("Load did not route the exact identity and locale: document=%+v port=%+v/%q", document, selected.loadIdentity, selected.loadLocale)
			}

			request := core.ApplyRequest{
				Protocol:                 core.ProtocolVersion,
				Profile:                  domain,
				Document:                 "document-handle",
				Locale:                   "en",
				ExpectedDocumentRevision: "revision-1",
				Operations:               []core.Operation{core.SetFieldOperation("root", "title", core.Text("Title"))},
			}
			result, err := registry.ExecuteMutation(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.DocumentRevision != "revision-2" || !reflect.DeepEqual(selected.executeRequest, request) {
				t.Fatalf("ExecuteMutation did not route the exact request: result=%+v request=%+v", result, selected.executeRequest)
			}

			for _, registration := range registrations {
				if registration.Domain == domain {
					continue
				}
				other := registration.Port.(*registryPort)
				if other.loadIdentity.Domain != "" || other.executeRequest.Profile != "" {
					t.Fatalf("domain %q received a request routed to %q", registration.Domain, domain)
				}
			}
		})
	}
}

func TestRegistryRoutesExactMutationWithoutEnteringPublicLoad(t *testing.T) {
	selected := &registryPort{
		validation: core.ValidationResult{Normalized: []core.Operation{
			core.SetFieldOperation("root", "title", core.Text("Title")),
		}},
		execution: core.ApplyResult{
			DocumentRevision: "revision-2", Changed: true,
			Changes: []core.Change{{Operation: 0, Kind: core.OperationSetField}},
			Normalized: []core.Operation{
				core.SetFieldOperation("root", "title", core.Text("Title")),
			},
		},
	}
	registry, err := NewRegistry(completeRegistryRegistrations(core.DomainPost, selected)...)
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.NewService(registry)
	if err != nil {
		t.Fatal(err)
	}
	request := core.ApplyRequest{
		Protocol:                 core.ProtocolVersion,
		Profile:                  core.DomainPost,
		Document:                 "post-handle",
		Locale:                   "en",
		ExpectedDocumentRevision: "revision-1",
		Operations: []core.Operation{
			core.SetFieldOperation("root", "title", core.Text("Title")),
		},
	}

	if _, err := service.Validate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Apply(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected.validateRequest, request) || !reflect.DeepEqual(selected.executeRequest, request) {
		t.Fatalf("exact requests were not routed: validate=%+v execute=%+v", selected.validateRequest, selected.executeRequest)
	}
	if selected.loadIdentity.Domain != "" {
		t.Fatalf("public load was entered: load=%+v", selected.loadIdentity)
	}
}

func TestRegistryPreservesPortErrors(t *testing.T) {
	loadFailure := errors.New("load failed")
	executeFailure := errors.New("execute failed")
	post := &registryPort{loadErr: loadFailure, executeErr: executeFailure}
	registry, err := NewRegistry(completeRegistryRegistrations(core.DomainPost, post)...)
	if err != nil {
		t.Fatal(err)
	}
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: "post-handle"}

	if _, err := registry.Load(context.Background(), identity, "en"); !errors.Is(err, loadFailure) {
		t.Fatalf("Load error = %v, want preserved port error", err)
	}
	request := core.ApplyRequest{
		Protocol: core.ProtocolVersion, Profile: core.DomainPost, Document: "post-handle",
		Locale: "en", ExpectedDocumentRevision: "revision-1",
	}
	if _, err := registry.ExecuteMutation(context.Background(), request); !errors.Is(err, executeFailure) {
		t.Fatalf("ExecuteMutation error = %v, want preserved port error", err)
	}
}

func TestRegistryRejectsUnsupportedRoutesAndMismatchedLoadIdentity(t *testing.T) {
	post := &registryPort{document: core.Document{
		Identity: core.DocumentIdentity{Domain: core.DomainPage, Reference: "post-handle"},
		Locale:   "en",
	}}
	registry, err := NewRegistry(completeRegistryRegistrations(core.DomainPost, post)...)
	if err != nil {
		t.Fatal(err)
	}

	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: "post-handle"}
	if _, err := registry.Load(context.Background(), identity, "en"); err == nil || !strings.Contains(err.Error(), "returned identity") {
		t.Fatalf("mismatched identity error = %v", err)
	}

	unsupported := core.DocumentIdentity{Domain: core.Domain("unknown"), Reference: "unknown-handle"}
	if _, err := registry.Load(context.Background(), unsupported, "en"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported Load error = %v", err)
	}
	if _, err := registry.ExecuteMutation(context.Background(), core.ApplyRequest{Profile: unsupported.Domain, Document: unsupported.Reference}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported ExecuteMutation error = %v", err)
	}
}

func TestRegistryRejectsNilAndUnregisteredRuntimeRoutes(t *testing.T) {
	identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: "post-handle"}
	var nilRegistry *Registry
	if _, err := nilRegistry.Load(context.Background(), identity, "en"); err == nil || !strings.Contains(err.Error(), "registry is required") {
		t.Fatalf("nil registry Load error = %v", err)
	}
	if _, err := (&Registry{}).ExecuteMutation(context.Background(), core.ApplyRequest{Profile: identity.Domain, Document: identity.Reference}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered ExecuteMutation error = %v", err)
	}
}

func TestRegistryRejectsMismatchedReferenceAndLocale(t *testing.T) {
	tests := []struct {
		name      string
		document  core.Document
		wantError string
	}{
		{
			name: "reference",
			document: core.Document{
				Identity: core.DocumentIdentity{Domain: core.DomainPost, Reference: "other-handle"},
				Locale:   "en",
			},
			wantError: "returned identity",
		},
		{
			name: "locale",
			document: core.Document{
				Identity: core.DocumentIdentity{Domain: core.DomainPost, Reference: "post-handle"},
				Locale:   "ko",
			},
			wantError: "returned locale",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			post := &registryPort{document: test.document}
			registry, err := NewRegistry(completeRegistryRegistrations(core.DomainPost, post)...)
			if err != nil {
				t.Fatal(err)
			}
			identity := core.DocumentIdentity{Domain: core.DomainPost, Reference: "post-handle"}
			if _, err := registry.Load(context.Background(), identity, "en"); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Load error = %v, want %q", err, test.wantError)
			}
		})
	}
}
