package ai

import (
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dependencycheck"
	"github.com/echovisionlab/geul-api/internal/llm"
	llmproviders "github.com/echovisionlab/geul-api/internal/llm/providers"
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

// MetadataJobManager runs queued metadata jobs for the worker: it calls the
// provider and recovers jobs whose delivery expired.
type MetadataJobManager struct {
	db             *gorm.DB
	provider       llm.Provider
	asyncPublisher AsyncPublisher
}

func NewMetadataJobManager(
	db *gorm.DB,
	googleAIAPIKey string,
	asyncPublisher AsyncPublisher,
) (*MetadataJobManager, error) {
	provider, err := newAITextProvider(googleAIAPIKey)
	if err != nil {
		return nil, err
	}
	return NewMetadataJobManagerWithProvider(db, provider, asyncPublisher), nil
}

func newAITextProvider(apiKey string) (llm.Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("googleAIAPIKey must be configured for gemini provider")
	}
	return llmproviders.NewGeminiProvider(llmproviders.GeminiConfig{APIKey: apiKey})
}

func NewMetadataJobManagerWithProvider(
	db *gorm.DB,
	provider llm.Provider,
	asyncPublisher AsyncPublisher,
) *MetadataJobManager {
	dependencycheck.New("ai.MetadataJobManager").
		RequireNotNil(db, "db").
		RequireNotNil(provider, "provider").
		RequireNotNil(asyncPublisher, "asyncPublisher").
		Validate()
	return &MetadataJobManager{
		db:             db,
		provider:       provider,
		asyncPublisher: asyncPublisher,
	}
}
