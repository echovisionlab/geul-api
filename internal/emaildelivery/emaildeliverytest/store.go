// Package emaildeliverytest provides the in-memory email delivery Store,
// its unit of work and the Store contract the PostgreSQL store keeps too.
package emaildeliverytest

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Store is the in-memory emaildelivery.Store. It knows no schema and takes
// no lock: its unit of work serializes transactions.
type Store struct {
	mu sync.Mutex
	state
	// Fail fails the Store method of that name with its error.
	Fail map[string]error
}

type state struct {
	adapters         map[string]emaildelivery.MailAdapter
	suppressions     map[string]emaildelivery.Suppression
	defaults         *emaildelivery.RenderDefaults
	recipientLocales map[string]string
	identityLocales  map[string]string
}

func (s state) clone() state {
	clone := s
	clone.adapters = mutationtest.ClonePersisted(s.adapters)
	clone.suppressions = mutationtest.ClonePersisted(s.suppressions)
	clone.defaults = mutationtest.ClonePersisted(s.defaults)
	clone.recipientLocales = mutationtest.ClonePersisted(s.recipientLocales)
	clone.identityLocales = mutationtest.ClonePersisted(s.identityLocales)
	return clone
}

var _ emaildelivery.Store = (*Store)(nil)

// NewStore returns an empty Store: no adapters, no suppressions and no site
// settings row.
func NewStore() *Store {
	return &Store{state: state{
		adapters: map[string]emaildelivery.MailAdapter{}, suppressions: map[string]emaildelivery.Suppression{},
		recipientLocales: map[string]string{}, identityLocales: map[string]string{},
	}}
}

// New returns an empty Store and the unit of work over it.
func New() (*Store, *mutationtest.UnitOfWork[emaildelivery.Store]) {
	store := NewStore()
	return store, mutationtest.New[emaildelivery.Store](store)
}

// Savepoint implements mutationtest.Savepointer.
func (s *Store) Savepoint() mutationtest.Checkpoint {
	s.mu.Lock()
	saved := s.state.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"adapters":         saved.adapters,
			"suppressions":     saved.suppressions,
			"defaults":         saved.defaults,
			"recipientLocales": saved.recipientLocales,
			"identityLocales":  saved.identityLocales,
		},
		Restore: func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.state = saved
		},
	}
}

// now is a stored timestamp: PostgreSQL keeps microseconds and no
// monotonic reading.
func now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (s *Store) fail(method string) error {
	if s.Fail == nil {
		return nil
	}
	return s.Fail[method]
}

// SeedMailAdapter stores adapter as it is, with an ID when it has none, and
// returns it.
func (s *Store) SeedMailAdapter(adapter emaildelivery.MailAdapter) emaildelivery.MailAdapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if adapter.ID == "" {
		adapter.ID = uuid.NewString()
	}
	s.adapters[adapter.ID] = adapter
	return adapter
}

// StoredMailAdapter returns the stored adapter id.
func (s *Store) StoredMailAdapter(id string) (emaildelivery.MailAdapter, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	adapter, ok := s.adapters[id]
	return adapter, ok
}

// MailAdapterCount is how many adapters are stored.
func (s *Store) MailAdapterCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.adapters)
}

// SeedSuppression stores suppression as it is, with an ID and timestamps
// when it has none, and returns it.
func (s *Store) SeedSuppression(suppression emaildelivery.Suppression) emaildelivery.Suppression {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insertSuppression(suppression)
}

// Suppressions returns every stored suppression of email, released or not.
func (s *Store) Suppressions(email string) []emaildelivery.Suppression {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []emaildelivery.Suppression
	for _, suppression := range s.suppressions {
		if strings.EqualFold(suppression.Email, email) {
			found = append(found, suppression)
		}
	}
	slices.SortFunc(found, func(a, b emaildelivery.Suppression) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return found
}

// SeedRenderDefaults stores the site settings an email renders with.
func (s *Store) SeedRenderDefaults(defaults emaildelivery.RenderDefaults) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaults = &defaults
}

// SeedRecipientLocale makes locale the preferred locale of the active Member
// whose primary email is recipient.
func (s *Store) SeedRecipientLocale(recipient, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recipientLocales[recipient] = locale
}

// SeedIdentityLocale makes locale the preferred locale of the active Member
// linked to identityID.
func (s *Store) SeedIdentityLocale(identityID, locale string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.identityLocales[identityID] = locale
}

func (s *Store) InsertMailAdapter(_ context.Context, adapter emaildelivery.MailAdapter) (emaildelivery.MailAdapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("InsertMailAdapter"); err != nil {
		return emaildelivery.MailAdapter{}, err
	}
	adapter.ID = uuid.NewString()
	if adapter.UpdatedAt == nil {
		at := now()
		adapter.UpdatedAt = &at
	}
	s.adapters[adapter.ID] = adapter
	return adapter, nil
}

func (s *Store) LockMailAdapter(_ context.Context, id string) (emaildelivery.MailAdapter, error) {
	return s.mailAdapter("LockMailAdapter", id)
}

func (s *Store) MailAdapter(_ context.Context, id string) (emaildelivery.MailAdapter, error) {
	return s.mailAdapter("MailAdapter", id)
}

func (s *Store) mailAdapter(method, id string) (emaildelivery.MailAdapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail(method); err != nil {
		return emaildelivery.MailAdapter{}, err
	}
	adapter, ok := s.adapters[id]
	if !ok {
		return emaildelivery.MailAdapter{}, emaildelivery.ErrMailAdapterNotFound
	}
	return adapter, nil
}

func (s *Store) UpdateMailAdapter(_ context.Context, id string, changes emaildelivery.MailAdapterChanges) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("UpdateMailAdapter"); err != nil {
		return err
	}
	adapter, ok := s.adapters[id]
	if !ok {
		return nil
	}
	if changes.Name != nil {
		adapter.Name = *changes.Name
	}
	if changes.IsActive != nil {
		adapter.IsActive = *changes.IsActive
	}
	if changes.Priority != nil {
		adapter.Priority = int(*changes.Priority)
	}
	if changes.Type != nil {
		adapter.Type = *changes.Type
	}
	if changes.Config != nil {
		adapter.Config = slices.Clone(changes.Config)
	}
	updatedAt := changes.UpdatedAt
	adapter.UpdatedAt = &updatedAt
	s.adapters[id] = adapter
	return nil
}

func (s *Store) DeleteMailAdapter(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("DeleteMailAdapter"); err != nil {
		return err
	}
	delete(s.adapters, id)
	return nil
}

func (s *Store) MailAdapters(_ context.Context, listing emaildelivery.MailAdapterListing) ([]emaildelivery.MailAdapter, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("MailAdapters"); err != nil {
		return nil, 0, err
	}
	var listed []emaildelivery.MailAdapter
	for _, adapter := range s.adapters {
		if !listing.ActiveOnly || adapter.IsActive {
			listed = append(listed, adapter)
		}
	}
	slices.SortFunc(listed, func(a, b emaildelivery.MailAdapter) int {
		return cmp.Or(cmp.Compare(a.Priority, b.Priority), a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	total := int64(len(listed))
	start := min(int(listing.Offset), len(listed))
	end := min(start+int(listing.Limit), len(listed))
	return slices.Clone(listed[start:end]), total, nil
}

// active returns the active suppression of email, which is normalized.
func (s *Store) active(email string) (emaildelivery.Suppression, bool) {
	for _, suppression := range s.suppressions {
		if strings.ToLower(suppression.Email) == email && suppression.ReleasedAt == nil {
			return suppression, true
		}
	}
	return emaildelivery.Suppression{}, false
}

func (s *Store) ActiveSuppression(_ context.Context, email string) (emaildelivery.Suppression, error) {
	return s.activeSuppression("ActiveSuppression", email)
}

func (s *Store) LockActiveSuppression(_ context.Context, email string) (emaildelivery.Suppression, error) {
	return s.activeSuppression("LockActiveSuppression", email)
}

func (s *Store) activeSuppression(method, email string) (emaildelivery.Suppression, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail(method); err != nil {
		return emaildelivery.Suppression{}, err
	}
	if suppression, ok := s.active(email); ok {
		return suppression, nil
	}
	return emaildelivery.Suppression{}, emaildelivery.ErrSuppressionNotFound
}

func (s *Store) ActiveSuppressions(_ context.Context, emails []string) ([]emaildelivery.Suppression, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("ActiveSuppressions"); err != nil {
		return nil, err
	}
	var found []emaildelivery.Suppression
	for _, email := range emails {
		if suppression, ok := s.active(email); ok {
			found = append(found, suppression)
		}
	}
	return found, nil
}

func (s *Store) SuppressionRecorded(_ context.Context, email, source, reason, referenceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("SuppressionRecorded"); err != nil {
		return false, err
	}
	for _, suppression := range s.suppressions {
		if strings.ToLower(suppression.Email) == email && suppression.Source == source && suppression.Reason == reason &&
			suppression.ReferenceID != nil && *suppression.ReferenceID == referenceID {
			return true, nil
		}
	}
	return false, nil
}

// errActiveSuppressionExists is the unique index on an address's active
// suppression refusing a second one.
var errActiveSuppressionExists = errors.New(`duplicate key value violates unique constraint "idx_email_suppression_active_email"`)

func (s *Store) InsertSuppression(_ context.Context, suppression emaildelivery.Suppression) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("InsertSuppression"); err != nil {
		return err
	}
	if _, exists := s.active(strings.ToLower(suppression.Email)); exists {
		return errActiveSuppressionExists
	}
	s.insertSuppression(emaildelivery.Suppression{
		Email: suppression.Email, Reason: suppression.Reason, Source: suppression.Source,
		ReferenceID: suppression.ReferenceID, LastError: suppression.LastError, SuppressedAt: suppression.SuppressedAt,
	})
	return nil
}

func (s *Store) insertSuppression(suppression emaildelivery.Suppression) emaildelivery.Suppression {
	if suppression.ID == "" {
		suppression.ID = uuid.NewString()
	}
	at := now()
	if suppression.SuppressedAt.IsZero() {
		suppression.SuppressedAt = at
	}
	if suppression.CreatedAt.IsZero() {
		suppression.CreatedAt = at
	}
	if suppression.UpdatedAt.IsZero() {
		suppression.UpdatedAt = at
	}
	s.suppressions[suppression.ID] = suppression
	return suppression
}

func (s *Store) ReplaceSuppression(_ context.Context, id string, replacement emaildelivery.Suppression) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("ReplaceSuppression"); err != nil {
		return err
	}
	suppression, ok := s.suppressions[id]
	if !ok {
		return nil
	}
	suppression.Reason, suppression.Source = replacement.Reason, replacement.Source
	suppression.ReferenceID, suppression.LastError = replacement.ReferenceID, replacement.LastError
	suppression.SuppressedAt, suppression.UpdatedAt = replacement.SuppressedAt, now()
	s.suppressions[id] = suppression
	return nil
}

func (s *Store) ReleaseSuppression(_ context.Context, id string, releasedAt time.Time, releasedBy *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("ReleaseSuppression"); err != nil {
		return err
	}
	suppression, ok := s.suppressions[id]
	if !ok {
		return nil
	}
	suppression.ReleasedAt, suppression.ReleasedBy, suppression.UpdatedAt = &releasedAt, releasedBy, now()
	s.suppressions[id] = suppression
	return nil
}

func (s *Store) EmailRenderDefaults(context.Context, string) (emaildelivery.RenderDefaults, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("EmailRenderDefaults"); err != nil {
		return emaildelivery.RenderDefaults{}, err
	}
	if s.defaults == nil {
		return emaildelivery.RenderDefaults{}, emaildelivery.ErrSiteSettingsNotFound
	}
	return *s.defaults, nil
}

func (s *Store) RecipientLocale(_ context.Context, recipient string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("RecipientLocale"); err != nil {
		return "", err
	}
	return s.recipientLocales[recipient], nil
}

func (s *Store) IdentityLocale(_ context.Context, identityID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail("IdentityLocale"); err != nil {
		return "", err
	}
	return s.identityLocales[identityID], nil
}
