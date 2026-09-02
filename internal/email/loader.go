package email

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"gorm.io/gorm"
)

// AdapterCacheTTL bounds cross-instance staleness when an ephemeral
// mail_adapter.changed invalidation is missed.
const AdapterCacheTTL = 30 * time.Second

// AdapterLoader loads and caches active mail adapters from the database.
// Broadcast invalidation is the fast path; a bounded TTL guarantees recovery
// after a missed anonymous-queue broadcast or subscriber reconnect.
type AdapterLoader struct {
	db       *gorm.DB
	factory  *AdapterFactory
	mu       sync.RWMutex
	adapters []Adapter
	loaded   bool
	loadedAt time.Time
	now      func() time.Time
	cacheTTL time.Duration
}

// NewAdapterLoader creates a new AdapterLoader.
func NewAdapterLoader(db *gorm.DB, factory *AdapterFactory) *AdapterLoader {
	if factory == nil {
		factory = NewAdapterFactory()
	}
	return &AdapterLoader{
		db:       db,
		factory:  factory,
		now:      time.Now,
		cacheTTL: AdapterCacheTTL,
	}
}

// GetActiveAdapters returns all active adapters, loading from DB if not yet loaded.
func (l *AdapterLoader) GetActiveAdapters(ctx context.Context) ([]Adapter, error) {
	// Check cache first
	l.mu.RLock()
	if adapterCacheFresh(l.loaded, l.loadedAt, l.now(), l.cacheTTL) {
		adapters := l.adapters
		l.mu.RUnlock()
		return adapters, nil
	}
	l.mu.RUnlock()

	// Cache miss, reload
	return l.reload(ctx)
}

// InvalidateCache clears the adapter cache, forcing a reload on next access.
func (l *AdapterLoader) InvalidateCache() {
	l.mu.Lock()
	previous := l.adapters
	l.adapters = nil
	l.loaded = false
	l.loadedAt = time.Time{}
	l.mu.Unlock()
	retireMailAdapters(previous)
	slog.Debug("Mail adapter cache invalidated")
}

// HandleMailAdapterChanged handles mail adapter changed events from PGMQ.
// It invalidates the cache so the next GetActiveAdapters call will reload from DB.
func (l *AdapterLoader) HandleMailAdapterChanged(ctx context.Context, body []byte) error {
	// We don't need to parse the event - just invalidate the cache
	l.InvalidateCache()
	slog.Info("Mail adapter cache invalidated due to change event")
	return nil
}

// reload loads active adapters from the database.
func (l *AdapterLoader) reload(ctx context.Context) ([]Adapter, error) {
	l.mu.Lock()

	// Double-check: another goroutine might have reloaded while waiting for lock
	if adapterCacheFresh(l.loaded, l.loadedAt, l.now(), l.cacheTTL) {
		adapters := l.adapters
		l.mu.Unlock()
		return adapters, nil
	}

	// Query active adapters ordered by priority (lower first)
	var dbAdapters []model.MailAdapter
	if err := l.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("priority ASC, created_at ASC").
		Find(&dbAdapters).Error; err != nil {
		l.mu.Unlock()
		return nil, err
	}

	// Build adapters
	adapters := make([]Adapter, 0, len(dbAdapters))
	for _, dbAdapter := range dbAdapters {
		adapter, err := l.factory.Create(&dbAdapter)
		if err != nil {
			slog.Error("Failed to create mail adapter",
				"domain", "mail",
				"event", "mail.adapter.load_failed",
				"adapter_id", dbAdapter.ID,
				"adapter_name", dbAdapter.Name,
				"adapter_type", dbAdapter.Type,
				"error", err,
			)
			l.mu.Unlock()
			retireMailAdapters(adapters)
			return nil, fmt.Errorf("active mail adapter %s is invalid: %w", dbAdapter.ID, err)
		}
		adapters = append(adapters, newManagedMailAdapter(adapter))
	}

	// Update cache
	previous := l.adapters
	l.adapters = adapters
	l.loaded = true
	l.loadedAt = l.now()
	l.mu.Unlock()
	retireMailAdapters(previous)

	slog.Info("Mail adapters loaded",
		"count", len(adapters),
		"active_ids", adapterIDs(adapters),
	)

	return adapters, nil
}

type closeableMailAdapter interface {
	Close() error
}

// managedMailAdapter prevents cache invalidation from closing an adapter while
// one of its provider calls is still in flight. A send that has not started
// when retirement happens fails retryably and reloads the DB-authoritative
// adapter set on the next command attempt.
type managedMailAdapter struct {
	adapter Adapter
	mu      sync.Mutex
	active  int
	retired bool
	closed  bool
}

func newManagedMailAdapter(adapter Adapter) Adapter {
	return &managedMailAdapter{adapter: adapter}
}

func (a *managedMailAdapter) ID() string                  { return a.adapter.ID() }
func (a *managedMailAdapter) Name() string                { return a.adapter.Name() }
func (a *managedMailAdapter) Type() model.MailAdapterType { return a.adapter.Type() }

func (a *managedMailAdapter) Send(ctx context.Context, message *Email) (*SendResult, error) {
	a.mu.Lock()
	if a.retired {
		a.mu.Unlock()
		return nil, NewDeliveryError(
			DeliveryErrorConnection,
			true,
			fmt.Errorf("mail adapter %s was retired", a.adapter.ID()),
		)
	}
	a.active++
	a.mu.Unlock()

	defer a.finishSend()
	return a.adapter.Send(ctx, message)
}

func (a *managedMailAdapter) finishSend() {
	a.mu.Lock()
	a.active--
	shouldClose := a.retired && a.active == 0 && !a.closed
	if shouldClose {
		a.closed = true
	}
	a.mu.Unlock()
	if shouldClose {
		closeMailAdapter(a.adapter)
	}
}

func (a *managedMailAdapter) retire() {
	a.mu.Lock()
	a.retired = true
	shouldClose := a.active == 0 && !a.closed
	if shouldClose {
		a.closed = true
	}
	a.mu.Unlock()
	if shouldClose {
		closeMailAdapter(a.adapter)
	}
}

func retireMailAdapters(adapters []Adapter) {
	for _, adapter := range adapters {
		if managed, ok := adapter.(*managedMailAdapter); ok {
			managed.retire()
		} else {
			closeMailAdapter(adapter)
		}
	}
}

func closeMailAdapter(adapter Adapter) {
	closer, ok := adapter.(closeableMailAdapter)
	if !ok || closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		slog.Warn("Failed to close retired mail adapter", "adapter_id", adapter.ID(), "error", err)
	}
}

func adapterCacheFresh(
	loaded bool,
	loadedAt time.Time,
	now time.Time,
	ttl time.Duration,
) bool {
	return loaded && !loadedAt.IsZero() && ttl > 0 && now.Sub(loadedAt) < ttl
}

// adapterIDs returns a slice of adapter IDs for logging.
func adapterIDs(adapters []Adapter) []string {
	ids := make([]string, len(adapters))
	for i, a := range adapters {
		ids[i] = a.ID()
	}
	return ids
}
