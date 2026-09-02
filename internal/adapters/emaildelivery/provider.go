package emaildeliveryadapter

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/email"
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
