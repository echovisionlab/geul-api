package emaildeliveryadapter

import (
	"context"
	"fmt"
	"io"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type MailAdapterLoader interface {
	GetActiveAdapters(context.Context) ([]email.Adapter, error)
}

// ProviderLoader adapts configured provider instances to the EmailDelivery
// application port. Provider priority and first-accept failover remain
// application policy.
type ProviderLoader struct {
	loader MailAdapterLoader
}

func NewProviderLoader(loader MailAdapterLoader) *ProviderLoader {
	return &ProviderLoader{loader: loader}
}

func (l *ProviderLoader) GetActiveAdapters(ctx context.Context) ([]email.Adapter, error) {
	if l == nil || l.loader == nil {
		return nil, fmt.Errorf("mail provider loader is required")
	}
	return l.loader.GetActiveAdapters(ctx)
}

// MailProviders opens the provider client of a mail adapter configuration
// through the production adapter factory, for the administrator use cases'
// verification and test sends.
type MailProviders struct{}

var _ emaildelivery.MailProviders = MailProviders{}

func (MailProviders) Open(adapter emaildelivery.MailAdapter) (emaildelivery.MailProvider, error) {
	sender, err := email.NewAdapterFactory().Create(&email.MailAdapter{
		ID: adapter.ID, Name: adapter.Name, Type: email.MailAdapterType(adapter.Type), IsActive: adapter.IsActive,
		Priority: adapter.Priority, Config: adapter.Config,
		CreatedAt: adapter.CreatedAt, UpdatedAt: adapter.UpdatedAt,
	})
	if err != nil {
		return nil, err
	}
	return openedProvider{sender}, nil
}

// openedProvider closes the connections a provider keeps pooled; a provider
// that pools none has nothing to close.
type openedProvider struct{ email.Adapter }

func (p openedProvider) Close() error {
	if closer, ok := p.Adapter.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// MailAdapterChangeQueue publishes the mail adapter change event other
// processes consume (mq.Publisher).
type MailAdapterChangeQueue interface {
	PublishMailAdapterChanged(context.Context, *managev1.MailAdapterChangedEvent) error
}

// MailAdapterChanges publishes committed mail adapter changes as events on
// queue.
type MailAdapterChanges struct {
	queue MailAdapterChangeQueue
}

var _ emaildelivery.MailAdapterChangePublisher = MailAdapterChanges{}

func NewMailAdapterChanges(queue MailAdapterChangeQueue) MailAdapterChanges {
	return MailAdapterChanges{queue: queue}
}

func (c MailAdapterChanges) PublishMailAdapterChanged(ctx context.Context, change emaildelivery.MailAdapterChange) error {
	return c.queue.PublishMailAdapterChanged(ctx, &managev1.MailAdapterChangedEvent{
		AdapterId: change.AdapterID, ChangeType: mailAdapterChangeType(change.Type), TimestampMs: change.At.UnixMilli(),
	})
}

func mailAdapterChangeType(changeType emaildelivery.MailAdapterChangeType) managev1.MailAdapterChangeType {
	switch changeType {
	case emaildelivery.MailAdapterCreated:
		return managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_CREATED
	case emaildelivery.MailAdapterUpdated:
		return managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_UPDATED
	case emaildelivery.MailAdapterDeleted:
		return managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_DELETED
	default:
		return managev1.MailAdapterChangeType_MAIL_ADAPTER_CHANGE_TYPE_UNSPECIFIED
	}
}
