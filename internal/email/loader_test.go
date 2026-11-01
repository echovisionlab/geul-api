package email

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAdapterCacheFreshExpiresMissedBroadcastInvalidation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

	require.True(t, adapterCacheFresh(true, now.Add(-29*time.Second), now, 30*time.Second))
	require.False(t, adapterCacheFresh(true, now.Add(-30*time.Second), now, 30*time.Second))
	require.False(t, adapterCacheFresh(false, now, now, 30*time.Second))
	require.False(t, adapterCacheFresh(true, time.Time{}, now, 30*time.Second))
}

// adapterRows is the mail_adapter table in memory.
type adapterRows struct {
	mu   sync.Mutex
	rows []model.MailAdapter
}

func (a *adapterRows) ActiveMailAdapters(context.Context) ([]model.MailAdapter, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.rows), nil
}

func (a *adapterRows) set(rows ...model.MailAdapter) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = rows
}

const sesTestConfig = `{"region":"us-east-1","access_key_id":"test","secret_access_key":"test","from_email":"sender@example.test"}`

func mailAdapterRow(id, name string, adapterType model.MailAdapterType, config string) model.MailAdapter {
	return model.MailAdapter{
		ID: id, Name: name, Type: adapterType, IsActive: true, Priority: 1,
		Config: model.MailAdapterConfig{RawMessage: json.RawMessage(config)},
	}
}

func TestAdapterLoaderRejectsInvalidAndNonDeliveryActiveAdaptersWithoutCaching(t *testing.T) {
	t.Parallel()
	store := &adapterRows{}
	store.set(mailAdapterRow("logging-1", "Logging", "MAIL_ADAPTER_TYPE_LOGGING", "{}"))

	loader := NewAdapterLoader(store, nil)
	_, err := loader.GetActiveAdapters(context.Background())
	require.ErrorContains(t, err, "non-delivery")

	store.set(mailAdapterRow("logging-1", "Logging", "MAIL_ADAPTER_TYPE_SES", sesTestConfig))
	adapters, err := loader.GetActiveAdapters(context.Background())
	require.NoError(t, err)
	require.Len(t, adapters, 1)
	require.Equal(t, "logging-1", adapters[0].ID())
}

func TestAdapterLoaderTTLReloadsDatabaseAfterMissedBroadcast(t *testing.T) {
	t.Parallel()
	store := &adapterRows{}
	store.set(mailAdapterRow("ses-1", "Before", "MAIL_ADAPTER_TYPE_SES", sesTestConfig))

	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	loader := NewAdapterLoader(store, nil)
	loader.now = func() time.Time { return now }

	adapters, err := loader.GetActiveAdapters(context.Background())
	require.NoError(t, err)
	require.Len(t, adapters, 1)
	require.Equal(t, "Before", adapters[0].Name())

	store.set(mailAdapterRow("ses-1", "After", "MAIL_ADAPTER_TYPE_SES", sesTestConfig))
	adapters, err = loader.GetActiveAdapters(context.Background())
	require.NoError(t, err)
	require.Equal(t, "Before", adapters[0].Name())

	now = now.Add(AdapterCacheTTL)
	adapters, err = loader.GetActiveAdapters(context.Background())
	require.NoError(t, err)
	require.Len(t, adapters, 1)
	require.Equal(t, "After", adapters[0].Name())
}

func TestAdapterLoaderInvalidationClosesRetiredSMTPPool(t *testing.T) {
	t.Parallel()
	retired := &SMTPAdapter{
		id:   "retired-smtp",
		pool: make(chan *smtpConn, smtpPoolSize),
	}
	loader := NewAdapterLoader(nil, nil)
	loader.adapters = []Adapter{retired}
	loader.loaded = true
	loader.loadedAt = time.Now()

	loader.InvalidateCache()

	require.True(t, retired.closed.Load())
	require.Nil(t, loader.adapters)
	require.False(t, loader.loaded)
}

func TestAdapterLoaderInvalidationWaitsForActiveSendBeforeClose(t *testing.T) {
	t.Parallel()
	adapter := &blockingCloseableAdapter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	loader := NewAdapterLoader(nil, nil)
	loader.adapters = []Adapter{newManagedMailAdapter(adapter)}
	loader.loaded = true
	loader.loadedAt = time.Now()

	done := make(chan error, 1)
	go func() {
		_, err := loader.adapters[0].Send(context.Background(), &Email{})
		done <- err
	}()
	<-adapter.started

	loader.InvalidateCache()
	require.Equal(t, int32(0), adapter.closeCount.Load())

	close(adapter.release)
	require.NoError(t, <-done)
	require.Equal(t, int32(1), adapter.closeCount.Load())
}

func TestManagedMailAdapterRejectsNewSendAfterRetirement(t *testing.T) {
	t.Parallel()
	adapter := &blockingCloseableAdapter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	managed := newManagedMailAdapter(adapter).(*managedMailAdapter)
	managed.retire()

	_, err := managed.Send(context.Background(), &Email{})
	require.Error(t, err)
	kind, retryable, ok := DeliveryErrorDecision(err)
	require.True(t, ok)
	require.True(t, retryable)
	require.Equal(t, DeliveryErrorConnection, kind)
	require.Equal(t, int32(1), adapter.closeCount.Load())
}

type blockingCloseableAdapter struct {
	started    chan struct{}
	release    chan struct{}
	closeCount atomic.Int32
}

func (a *blockingCloseableAdapter) ID() string   { return "blocking" }
func (a *blockingCloseableAdapter) Name() string { return "Blocking" }
func (a *blockingCloseableAdapter) Type() model.MailAdapterType {
	return model.MailAdapterType("test")
}
func (a *blockingCloseableAdapter) Send(context.Context, *Email) (*SendResult, error) {
	close(a.started)
	<-a.release
	return &SendResult{MessageID: "accepted"}, nil
}
func (a *blockingCloseableAdapter) Close() error {
	a.closeCount.Add(1)
	return nil
}
