package account

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type lifecycleDependencyIdentityManager struct {
	auth.IdentityManager
	identity *auth.Identity
}

func (m *lifecycleDependencyIdentityManager) GetIdentity(
	_ context.Context,
	identityID string,
) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != identityID {
		return nil, errors.New("identity not found")
	}
	return m.identity, nil
}

type lifecycleDependencyPublisher struct {
}

func (p *lifecycleDependencyPublisher) PublishSendEmail(
	_ context.Context,
	job *managev1.SendEmailEvent,
) error {
	return nil
}

func TestNewAccountLifecycleServiceRejectsMissingDependencies(t *testing.T) {
	manager := &lifecycleDependencyIdentityManager{}
	publisher := &lifecycleDependencyPublisher{}

	require.Panics(t, func() {
		NewAccountLifecycleService(nil, manager, nil, "https://www.example.test", publisher)
	})
	require.Panics(t, func() {
		NewAccountLifecycleService(&gorm.DB{}, nil, nil, "https://www.example.test", publisher)
	})
	require.Panics(t, func() {
		NewAccountLifecycleService(&gorm.DB{}, manager, nil, "https://www.example.test", nil)
	})
}
