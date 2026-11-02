package accounttest

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// Kratos is the Kratos admin API over in-memory Identities. A deleted
// Identity also leaves the fake store, as Kratos' deletion does through the
// schema. Calls lists every admin call in order, as "Method:identityID".
type Kratos struct {
	mu         sync.Mutex
	store      *Store
	identities map[string]*auth.Identity
	calls      []string
	failures   map[string]error
	byIdentity map[string]error
	hooks      map[string]func()
}

var (
	_ account.Identities   = (*Kratos)(nil)
	_ auth.IdentityDeleter = (*Kratos)(nil)
)

// NewKratos returns a Kratos over store's Identities.
func NewKratos(store *Store) *Kratos {
	return &Kratos{store: store, identities: map[string]*auth.Identity{}, failures: map[string]error{}, byIdentity: map[string]error{}, hooks: map[string]func(){}}
}

// Put stores an Identity.
func (k *Kratos) Put(identity *auth.Identity) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.identities[identity.ID] = clone(identity)
}

// Identity returns a copy of a stored Identity, nil when it is gone.
func (k *Kratos) Identity(identityID string) *auth.Identity {
	k.mu.Lock()
	defer k.mu.Unlock()
	return clone(k.identities[identityID])
}

// Update changes a stored Identity in place, as a Kratos flow does.
func (k *Kratos) Update(identityID string, change func(*auth.Identity)) {
	k.mu.Lock()
	defer k.mu.Unlock()
	change(k.identities[identityID])
}

// FailIdentity makes every later call about identityID answer err; nil
// clears it.
func (k *Kratos) FailIdentity(identityID string, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err == nil {
		delete(k.byIdentity, identityID)
		return
	}
	k.byIdentity[identityID] = err
}

// Fail makes every later call of the named method answer err; nil clears
// it.
func (k *Kratos) Fail(method string, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err == nil {
		delete(k.failures, method)
		return
	}
	k.failures[method] = err
}

// OnCall runs hook before every later call of the named method.
func (k *Kratos) OnCall(method string, hook func()) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.hooks[method] = hook
}

// Calls returns every admin call so far, in order.
func (k *Kratos) Calls() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.calls)
}

// Writes returns the calls that change Kratos, in order.
func (k *Kratos) Writes() []string {
	var writes []string
	for _, call := range k.Calls() {
		if !strings.HasPrefix(call, "Get") && !strings.HasPrefix(call, "Find") {
			writes = append(writes, call)
		}
	}
	return writes
}

func clone(identity *auth.Identity) *auth.Identity {
	if identity == nil {
		return nil
	}
	copied := *identity
	copied.Traits = maps.Clone(identity.Traits)
	copied.MetadataAdmin = maps.Clone(identity.MetadataAdmin)
	copied.MetadataPublic = maps.Clone(identity.MetadataPublic)
	copied.VerifiableAddresses = slices.Clone(identity.VerifiableAddresses)
	copied.Credentials = make(map[string]auth.Credential, len(identity.Credentials))
	for key, credential := range identity.Credentials {
		credential.Identifiers = slices.Clone(credential.Identifiers)
		copied.Credentials[key] = credential
	}
	return &copied
}

func (k *Kratos) enter(method, identityID string) error {
	k.mu.Lock()
	hook := k.hooks[method]
	k.mu.Unlock()
	if hook != nil {
		hook()
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, method+":"+identityID)
	if err := k.failures[method]; err != nil {
		return err
	}
	return k.byIdentity[identityID]
}

func notFound(identityID string) error {
	return fmt.Errorf("identity %s: %w", identityID, &auth.KratosHTTPError{StatusCode: http.StatusNotFound, Body: "identity not found"})
}

func (k *Kratos) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if err := k.enter("GetIdentity", identityID); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	identity, found := k.identities[identityID]
	if !found {
		return nil, notFound(identityID)
	}
	return clone(identity), nil
}

func (k *Kratos) GetIdentityWithIncludeCredential(_ context.Context, identityID, credentialType string) (*auth.Identity, error) {
	if err := k.enter("GetIdentityWithIncludeCredential", identityID); err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	identity, found := k.identities[identityID]
	if !found {
		return nil, notFound(identityID)
	}
	scoped := clone(identity)
	scoped.Credentials = map[string]auth.Credential{}
	if credential, ok := identity.Credentials[credentialType]; ok {
		credential.Identifiers = slices.Clone(credential.Identifiers)
		scoped.Credentials[credentialType] = credential
	}
	return scoped, nil
}

func (k *Kratos) FindIdentityByCredentialIdentifier(_ context.Context, identifier string) (*auth.Identity, bool, error) {
	if err := k.enter("FindIdentityByCredentialIdentifier", identifier); err != nil {
		return nil, false, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	ids := slices.Sorted(maps.Keys(k.identities))
	for _, id := range ids {
		for _, credential := range k.identities[id].Credentials {
			for _, existing := range credential.Identifiers {
				if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(identifier)) {
					return clone(k.identities[id]), true, nil
				}
			}
		}
	}
	return nil, false, nil
}

func (k *Kratos) update(identityID string, change func(*auth.Identity)) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	identity, found := k.identities[identityID]
	if !found {
		return notFound(identityID)
	}
	change(identity)
	return nil
}

func (k *Kratos) UpdateIdentityAccountEmailState(_ context.Context, identityID string, currentEmail *string, traits structured.Fields, addresses []auth.VerifiableAddress) error {
	if err := k.enter("UpdateIdentityAccountEmailState", identityID); err != nil {
		return err
	}
	err := k.update(identityID, func(identity *auth.Identity) {
		if identity.Traits == nil {
			identity.Traits = structured.Fields{}
		}
		if identity.Credentials == nil {
			identity.Credentials = map[string]auth.Credential{}
		}
		if currentEmail != nil {
			email := strings.ToLower(strings.TrimSpace(*currentEmail))
			identity.Traits["email"] = email
			identity.Credentials["code"] = auth.Credential{Type: "code", Identifiers: []string{email}}
		}
		for key, value := range traits {
			if value == nil {
				delete(identity.Traits, key)
			} else {
				identity.Traits[key] = value
			}
		}
		if addresses != nil {
			identity.VerifiableAddresses = slices.Clone(addresses)
		}
	})
	if err == nil && currentEmail != nil && k.store != nil {
		email := strings.ToLower(strings.TrimSpace(*currentEmail))
		k.store.UpdateIdentity(identityID, func(row *Identity) { row.Email = email })
	}
	return err
}

func (k *Kratos) DeleteIdentityCredentialIdentifier(_ context.Context, identityID, credentialType, identifier string) error {
	if err := k.enter("DeleteIdentityCredentialIdentifier", identityID); err != nil {
		return err
	}
	return k.update(identityID, func(identity *auth.Identity) {
		credential, ok := identity.Credentials[credentialType]
		if !ok {
			return
		}
		provider, subject, ok := strings.Cut(identifier, ":")
		if !ok {
			return
		}
		next := make([]string, 0, len(credential.Identifiers))
		for _, existing := range credential.Identifiers {
			existingProvider, existingSubject, found := strings.Cut(strings.TrimSpace(existing), ":")
			if !found || !strings.EqualFold(existingProvider, provider) || existingSubject != subject {
				next = append(next, existing)
			}
		}
		credential.Identifiers = next
		if len(next) == 0 {
			delete(identity.Credentials, credentialType)
		} else {
			identity.Credentials[credentialType] = credential
		}
	})
}

func (k *Kratos) UpdateIdentityMetadataAdmin(_ context.Context, identityID string, metadata structured.Fields) error {
	if err := k.enter("UpdateIdentityMetadataAdmin", identityID); err != nil {
		return err
	}
	err := k.update(identityID, func(identity *auth.Identity) { identity.MetadataAdmin = maps.Clone(metadata) })
	if err == nil && k.store != nil {
		banned, _ := metadata["banned"].(bool)
		k.store.UpdateIdentity(identityID, func(row *Identity) { row.Banned = banned })
	}
	return err
}

func (k *Kratos) SetIdentityState(_ context.Context, identityID, state string) error {
	if err := k.enter("SetIdentityState", identityID); err != nil {
		return err
	}
	err := k.update(identityID, func(identity *auth.Identity) { identity.State = state })
	if err == nil && k.store != nil {
		k.store.UpdateIdentity(identityID, func(row *Identity) { row.State = state })
	}
	return err
}

func (k *Kratos) DeleteIdentitySessions(_ context.Context, identityID string) error {
	if err := k.enter("DeleteIdentitySessions", identityID); err != nil {
		return err
	}
	if k.store != nil {
		k.store.UpdateIdentity(identityID, func(row *Identity) { row.LiveSessions = nil })
	}
	return nil
}

func (k *Kratos) DeleteSession(_ context.Context, sessionID string) error {
	if err := k.enter("DeleteSession", sessionID); err != nil {
		return err
	}
	if k.store != nil {
		k.store.mu.Lock()
		for id, identity := range k.store.state.identities {
			identity.LiveSessions = slices.DeleteFunc(slices.Clone(identity.LiveSessions), func(session account.Session) bool { return session.ID == sessionID })
			k.store.state.identities[id] = identity
		}
		k.store.mu.Unlock()
	}
	return nil
}

func (k *Kratos) DeleteIdentity(_ context.Context, identityID string) error {
	if err := k.enter("DeleteIdentity", identityID); err != nil {
		return err
	}
	k.mu.Lock()
	delete(k.identities, identityID)
	k.mu.Unlock()
	if k.store != nil {
		k.store.DeleteIdentity(identityID)
	}
	return nil
}
