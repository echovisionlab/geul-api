//go:build integration

package emaildelivery

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

// newTestService builds the email delivery use cases through NewService.
// Fields left unset in deps get stand-ins: a cache that ignores
// invalidation, a change publisher that fails if reached, and a recording
// audit appender.
func newTestService(t *testing.T, deps Deps) *Service {
	t.Helper()
	if deps.SpiceDB == nil {
		deps.SpiceDB = &auth.SpiceDBClient{}
	}
	if deps.AdapterCache == nil {
		deps.AdapterCache = ignoredAdapterCache{}
	}
	if deps.AdapterChanges == nil {
		deps.AdapterChanges = unusedAdapterChanges{}
	}
	if deps.Audit == nil {
		deps.Audit = &audittest.Recorder{}
	}
	service, err := NewService(deps)
	require.NoError(t, err)
	return service
}

type ignoredAdapterCache struct{}

func (ignoredAdapterCache) InvalidateCache() {}

type unusedAdapterChanges struct{}

func (unusedAdapterChanges) PublishMailAdapterChanged(context.Context, *managev1.MailAdapterChangedEvent) error {
	return errors.New("test did not configure the mail adapter change publisher")
}

// mailAdapterTestClient serves the mail adapter RPCs and the MCP sender
// update with Connect errors, the shape the characterization tests assert.
type mailAdapterTestClient struct {
	*MailAdapterConnectHandler
	service *Service
}

func newMailAdapterTestClient(t *testing.T, deps Deps) mailAdapterTestClient {
	t.Helper()
	service := newTestService(t, deps)
	return mailAdapterTestClient{MailAdapterConnectHandler: NewMailAdapterConnectHandler(service), service: service}
}

func (c mailAdapterTestClient) UpdateSender(ctx context.Context, input MailAdapterSenderUpdate) (*connect.Response[managev1.UpdateMailAdapterResponse], error) {
	adapter, err := c.service.UpdateMailAdapterSender(ctx, input)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.UpdateMailAdapterResponse{Adapter: adapter}), nil
}
