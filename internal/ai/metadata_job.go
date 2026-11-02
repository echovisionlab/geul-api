package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/llm"
	llmproviders "github.com/echovisionlab/geul-api/internal/llm/providers"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

const (
	metadataAIJobLeaseDuration   = 3 * time.Minute
	metadataAIJobRecoveryDelay   = 10 * time.Minute
	metadataAIPersistenceTimeout = 5 * time.Second
	metadataAIJobRecoveryBatch   = 25
)

// MetadataJobManager runs queued metadata jobs for the worker: it calls the
// provider and recovers jobs whose delivery expired.
type MetadataJobManager struct {
	units     mutation.UnitOfWork[Store]
	provider  llm.Provider
	mutations *mutation.StoreRunner[Store]
}

// JobManagerDeps are the collaborators of the metadata job runner; all are
// required. Provider generates the suggestions.
type JobManagerDeps struct {
	Units    mutation.UnitOfWork[Store]
	SpiceDB  mutation.Authorizer
	Fence    identitystate.Fence
	Audit    domainaudit.Appender
	Provider llm.Provider
}

// NewMetadataJobManager returns the job runner over deps.
func NewMetadataJobManager(deps JobManagerDeps) (*MetadataJobManager, error) {
	if deps.Provider == nil {
		return nil, fmt.Errorf("metadata AI job provider is required")
	}
	mutations, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &MetadataJobManager{units: deps.Units, provider: deps.Provider, mutations: mutations}, nil
}

// NewGeminiProvider returns the metadata provider over the Google AI key.
func NewGeminiProvider(apiKey string) (llm.Provider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("googleAIAPIKey must be configured for gemini provider")
	}
	return llmproviders.NewGeminiProvider(llmproviders.GeminiConfig{APIKey: apiKey})
}

// runnerAccess authorizes a runner write whose own condition on the job's
// row decides it: the request that queued the job was authorized.
var runnerAccess = mutation.Checked(func(context.Context, Store) error { return nil })

// runJobUpdate runs one conditional write of the runner, which answers
// whether its condition still held.
func (m *MetadataJobManager) runJobUpdate(ctx context.Context, update func(context.Context, Store) (bool, error)) (bool, error) {
	return mutation.Run(ctx, m.mutations, mutation.StoreSpec[bool, Store]{
		Access: runnerAccess,
		Change: update,
		// Metadata jobs are operational queue rows, not audited content.
		Audit: func(bool) mutation.Audit { return mutation.Unchanged() },
	})
}
