package aidocumentadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
)

// DomainRegistration binds one DCDP/1 document domain to its owning-domain
// implementation. NewRegistry requires one and only one registration for every
// supported domain so a partially wired server cannot start.
type DomainRegistration struct {
	Domain core.Domain
	Port   core.DomainPort
}

// Registry is the fail-closed routing boundary between the shared AI document
// application and the owning-domain implementations.
type Registry struct {
	ports map[core.Domain]core.DomainPort
}

// NewRegistry validates and freezes the complete owning-domain routing table.
func NewRegistry(registrations ...DomainRegistration) (*Registry, error) {
	domains := core.SupportedDomains()
	domainSet := make(map[core.Domain]struct{}, len(domains))
	for _, domain := range domains {
		domainSet[domain] = struct{}{}
	}
	ports := make(map[core.Domain]core.DomainPort, len(domains))
	for index, registration := range registrations {
		if _, supported := domainSet[registration.Domain]; !supported {
			return nil, fmt.Errorf("AI document registration %d has unsupported domain %q", index, registration.Domain)
		}
		if domainPortIsNil(registration.Port) {
			return nil, fmt.Errorf("AI document domain %q port is required", registration.Domain)
		}
		if _, duplicate := ports[registration.Domain]; duplicate {
			return nil, fmt.Errorf("AI document domain %q is registered more than once", registration.Domain)
		}
		ports[registration.Domain] = registration.Port
	}

	missing := make([]string, 0, len(domains)-len(ports))
	for _, domain := range domains {
		if _, registered := ports[domain]; !registered {
			missing = append(missing, string(domain))
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf("AI document domain ports are missing: %s", strings.Join(missing, ", "))
	}
	return &Registry{ports: ports}, nil
}

func (r *Registry) Load(
	ctx context.Context,
	identity core.DocumentIdentity,
	locale core.Locale,
) (core.Document, error) {
	port, err := r.domainPort(identity.Domain)
	if err != nil {
		return core.Document{}, err
	}
	document, err := port.Load(ctx, identity, locale)
	if err != nil {
		return core.Document{}, err
	}
	if document.Identity != identity {
		return core.Document{}, fmt.Errorf(
			"AI document domain %q returned identity %q/%q for %q/%q",
			identity.Domain,
			document.Identity.Domain,
			document.Identity.Reference,
			identity.Domain,
			identity.Reference,
		)
	}
	if document.Locale != locale {
		return core.Document{}, fmt.Errorf(
			"AI document domain %q returned locale %q for %q",
			identity.Domain,
			document.Locale,
			locale,
		)
	}
	return document, nil
}

// LoadBatch reads each domain's documents through its own port, together when that
// port can. The domains read concurrently, each in its own snapshot.
func (r *Registry) LoadBatch(
	ctx context.Context,
	authorized func(core.DocumentReference) bool,
	requests []core.LoadRequest,
) ([]core.DeferredDocument, error) {
	positions := make(map[core.Domain][]int)
	for position, request := range requests {
		positions[request.Document.Domain] = append(positions[request.Document.Domain], position)
	}
	documents := make([]core.DeferredDocument, len(requests))
	var group sync.WaitGroup
	for domain, domainPositions := range positions {
		port, err := r.domainPort(domain)
		if err != nil {
			for _, position := range domainPositions {
				documents[position] = func() (core.Document, error) { return core.Document{}, err }
			}
			continue
		}
		group.Go(func() {
			domainRequests := make([]core.LoadRequest, len(domainPositions))
			for index, position := range domainPositions {
				domainRequests[index] = requests[position]
			}
			loaded := core.LoadBatch(ctx, port, authorized, domainRequests)
			for index, position := range domainPositions {
				documents[position] = loaded[index]
			}
		})
	}
	group.Wait()
	return documents, nil
}

// ValidateMutation routes the request directly to the owning domain's locked
// validation transaction without entering its public read projection.
func (r *Registry) ValidateMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ValidationResult, error) {
	port, err := r.domainPort(request.Identity().Domain)
	if err != nil {
		return core.ValidationResult{}, err
	}
	return port.ValidateMutation(ctx, request)
}

// ExecuteMutation routes an apply directly to the owning domain's locked
// mutation transaction without a Load-then-Apply compatibility path.
func (r *Registry) ExecuteMutation(
	ctx context.Context,
	request core.ApplyRequest,
) (core.ApplyResult, error) {
	port, err := r.domainPort(request.Identity().Domain)
	if err != nil {
		return core.ApplyResult{}, err
	}
	return port.ExecuteMutation(ctx, request)
}

func (r *Registry) domainPort(domain core.Domain) (core.DomainPort, error) {
	if r == nil {
		return nil, errors.New("AI document domain registry is required")
	}
	supported := false
	for _, candidate := range core.SupportedDomains() {
		if candidate == domain {
			supported = true
			break
		}
	}
	if !supported {
		return nil, fmt.Errorf("unsupported AI document domain %q", domain)
	}
	port, registered := r.ports[domain]
	if !registered || domainPortIsNil(port) {
		return nil, fmt.Errorf("AI document domain %q is not registered", domain)
	}
	return port, nil
}

func domainPortIsNil(port core.DomainPort) bool {
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

var _ core.DomainPort = (*Registry)(nil)
var _ core.ExactMutationPort = (*Registry)(nil)
