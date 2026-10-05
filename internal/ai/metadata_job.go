package ai

import (
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/llm"
	"gorm.io/gorm"
)

const (
	metadataAIJobStatusQueued    = "queued"
	metadataAIJobStatusRunning   = "running"
	metadataAIJobStatusReady     = "ready"
	metadataAIJobStatusFailed    = "failed"
	metadataAIJobStatusApplied   = "applied"
	metadataAIJobStatusDismissed = "dismissed"

	metadataAIJobLeaseDuration   = 3 * time.Minute
	metadataAIJobRecoveryDelay   = 10 * time.Minute
	metadataAIPersistenceTimeout = 5 * time.Second
	metadataAIJobRecoveryBatch   = 25
)

type MetadataJobManager struct {
	db             *gorm.DB
	spiceDB        *auth.SpiceDBClient
	provider       llm.Provider
	asyncPublisher AsyncPublisher
}

func NewMetadataJobManager(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	googleAIAPIKey string,
	asyncPublisher AsyncPublisher,
) (*MetadataJobManager, error) {
	provider, err := newAITextProvider(googleAIAPIKey)
	if err != nil {
		return nil, err
	}
	return NewMetadataJobManagerWithProvider(db, spiceDB, provider, asyncPublisher), nil
}

func newAITextProvider(apiKey string) (llm.Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("googleAIAPIKey must be configured for gemini provider")
	}
	return llm.NewGeminiProvider(llm.GeminiConfig{APIKey: apiKey})
}

func NewMetadataJobManagerWithProvider(
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	provider llm.Provider,
	asyncPublisher AsyncPublisher,
) *MetadataJobManager {
	dependencycheck.New("ai.MetadataJobManager").
		RequireNotNil(db, "db").
		RequireNotNil(spiceDB, "spiceDB").
		RequireNotNil(provider, "provider").
		RequireNotNil(asyncPublisher, "asyncPublisher").
		Validate()
	return &MetadataJobManager{
		db:             db,
		spiceDB:        spiceDB,
		provider:       provider,
		asyncPublisher: asyncPublisher,
	}
}
