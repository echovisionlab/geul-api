package email

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAdapterCacheFreshExpiresMissedBroadcastInvalidation(t *testing.T) {
	now := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)

	require.True(t, adapterCacheFresh(true, now.Add(-29*time.Second), now, 30*time.Second))
	require.False(t, adapterCacheFresh(true, now.Add(-30*time.Second), now, 30*time.Second))
	require.False(t, adapterCacheFresh(false, now, now, 30*time.Second))
	require.False(t, adapterCacheFresh(true, time.Time{}, now, 30*time.Second))
}

func TestAdapterLoaderRejectsInvalidAndNonDeliveryActiveAdaptersWithoutCaching(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:adapter-loader?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE mail_adapter (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			is_active BOOLEAN NOT NULL,
			priority INTEGER NOT NULL,
			config BLOB NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO mail_adapter (id, name, type, is_active, priority, config, created_at)
		VALUES ('logging-1', 'Logging', 'MAIL_ADAPTER_TYPE_LOGGING', TRUE, 1, '{}', CURRENT_TIMESTAMP)
	`).Error)

	loader := NewAdapterLoader(db, nil)
	_, err = loader.GetActiveAdapters(context.Background())
	require.ErrorContains(t, err, "non-delivery")

	require.NoError(t, db.Exec(`
		UPDATE mail_adapter
		SET type = 'MAIL_ADAPTER_TYPE_SES',
			config = '{"region":"us-east-1","access_key_id":"test","secret_access_key":"test","from_email":"sender@example.test"}'
		WHERE id = 'logging-1'
	`).Error)
	adapters, err := loader.GetActiveAdapters(context.Background())
	require.NoError(t, err)
	require.Len(t, adapters, 1)
	require.Equal(t, "logging-1", adapters[0].ID())
}

func TestAdapterLoaderTTLReloadsDatabaseAfterMissedBroadcast(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:adapter-loader-ttl?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE mail_adapter (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			is_active BOOLEAN NOT NULL,
			priority INTEGER NOT NULL,
			config BLOB NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME
		)
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO mail_adapter (id, name, type, is_active, priority, config, created_at)
		VALUES (
			'ses-1',
			'Before',
			'MAIL_ADAPTER_TYPE_SES',
			TRUE,
			1,
			'{"region":"us-east-1","access_key_id":"test","secret_access_key":"test","from_email":"sender@example.test"}',
			CURRENT_TIMESTAMP
		)
	`).Error)

	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	loader := NewAdapterLoader(db, nil)
	loader.now = func() time.Time { return now }

	adapters, err := loader.GetActiveAdapters(context.Background())
	require.NoError(t, err)
	require.Len(t, adapters, 1)
	require.Equal(t, "Before", adapters[0].Name())

	require.NoError(t, db.Exec(`UPDATE mail_adapter SET name = 'After' WHERE id = 'ses-1'`).Error)
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
