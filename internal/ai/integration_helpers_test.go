//go:build integration

package ai

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start AI integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close AI integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func newServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationTest(t)
	require.NotNil(t, stack)
	return stack.DB
}

func newConcurrentServiceIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	require.NotNil(t, stack)
	db, err := gorm.Open(postgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

type capturingAsyncPublisher struct {
	messages               []publishedMessage
	transactionalExecutors []eventpkg.DBTX
	confirmedErr           error
}

type publishedMessage struct {
	key         string
	messageID   string
	body        []byte
	messageType string
}

func (c *capturingAsyncPublisher) EnqueueProtobufWithExecutor(
	_ context.Context,
	executor eventpkg.DBTX,
	queue string,
	messageID string,
	message proto.Message,
) error {
	if executor == nil {
		return errors.New("transactional executor is required")
	}
	c.transactionalExecutors = append(c.transactionalExecutors, executor)
	body, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	c.messages = append(c.messages, publishedMessage{
		key:         queue,
		messageID:   messageID,
		body:        body,
		messageType: string(message.ProtoReflect().Descriptor().FullName()),
	})
	return c.confirmedErr
}

func decodePublishedRoutedMessages[T proto.Message](
	t *testing.T,
	messages []publishedMessage,
	_ string,
	routingKey string,
	newMessage func() T,
) []T {
	t.Helper()
	decoded := make([]T, 0)
	for _, message := range messages {
		if message.key != routingKey {
			continue
		}
		target := newMessage()
		if message.messageType != string(target.ProtoReflect().Descriptor().FullName()) {
			continue
		}
		require.NoError(t, proto.Unmarshal(message.body, target))
		decoded = append(decoded, target)
	}
	return decoded
}

func seedActiveMemberEmailPair(t *testing.T, db *gorm.DB, identityID, email string) string {
	t.Helper()
	memberID := uuid.NewString()
	now := time.Now().UTC()
	require.NoError(t, db.Exec(
		"UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid",
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id, created_at)
		 SELECT id, created_at FROM kratos.identities WHERE id = ?::uuid
		 ON CONFLICT (id) DO NOTHING`,
		identityID,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID: memberID, AccountIdentityID: &identityID, Nickname: "Metadata AI fixture " + memberID,
		Onboarded: true, PrimaryEmail: &email, AvailableEmails: []string{email},
		SocialLinks: map[string]string{}, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return memberID
}

type stubAIProvider struct {
	text        string
	err         error
	calls       int
	lastRequest llm.GenerationRequest
}

type stubAIProviderSession struct {
	provider *stubAIProvider
	spec     llm.SessionSpec
}

func (s *stubAIProvider) GenerateText(_ context.Context, req llm.GenerationRequest) (string, error) {
	s.calls++
	s.lastRequest = req
	if s.err != nil {
		return "", s.err
	}
	return s.text, nil
}

func (s *stubAIProvider) StartSession(_ context.Context, spec llm.SessionSpec) (llm.Session, error) {
	return &stubAIProviderSession{provider: s, spec: spec}, nil
}

func (*stubAIProvider) ProviderName() string { return "stub" }

func (*stubAIProvider) ModelName() string { return "stub-model" }

func (s *stubAIProviderSession) GenerateText(
	ctx context.Context,
	turn llm.SessionTurn,
) (string, error) {
	return s.provider.GenerateText(ctx, llm.GenerationRequest{
		RequestID:          s.spec.RequestID,
		Action:             s.spec.Action,
		SystemPrompt:       s.spec.SystemPrompt,
		UserPrompt:         turn.UserPrompt,
		ResponseJSONSchema: s.spec.ResponseJSONSchema,
	})
}

func (*stubAIProviderSession) Close(context.Context) error { return nil }
