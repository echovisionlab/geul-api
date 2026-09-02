package og

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Resolver routes one manual generation request to its owning entity source.
type Resolver struct {
	sources []RequestSource
}

func NewResolver(sources ...RequestSource) *Resolver {
	if len(sources) == 0 {
		panic("OG request sources are required")
	}
	return &Resolver{sources: sources}
}

func (r *Resolver) Resolve(
	ctx context.Context,
	db *gorm.DB,
	request *managev1.RegenerateOgImageRequest,
) ([]Request, error) {
	if request == nil {
		return nil, errs.Required("request")
	}
	policy, ok := PolicyForEntityType(request.GetEntityType())
	if !ok {
		return nil, errs.InvalidEntityType(request.GetEntityType().String())
	}
	if !SupportsNewGeneration(policy) {
		return nil, errs.FailedPrecondition("OG generation is disabled for this entity")
	}
	for _, source := range r.sources {
		if source != nil && source.Handles(policy.Name) {
			requests, err := source.Resolve(ctx, db, policy.Name, request.GetEntityId(), request.GetSelection())
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.NotFound(policy.Name, strings.TrimSpace(request.GetEntityId()))
			}
			return requests, err
		}
	}
	return nil, errs.InvalidEntityType(policy.Name)
}

// Collector combines independently owned entity snapshots for global work.
type Collector struct {
	sources []AllRequestSource
}

func NewCollector(sources ...AllRequestSource) *Collector {
	if len(sources) == 0 {
		panic("OG all-request sources are required")
	}
	return &Collector{sources: sources}
}

func (c *Collector) Collect(ctx context.Context, db *gorm.DB) ([]Request, error) {
	requests := make([]Request, 0)
	for _, source := range c.sources {
		if source == nil {
			continue
		}
		current, err := source.All(ctx, db)
		if err != nil {
			return nil, err
		}
		requests = append(requests, current...)
	}
	return dedupeRequests(requests), nil
}

func dedupeRequests(requests []Request) []Request {
	result := make([]Request, 0, len(requests))
	seen := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		key := strings.TrimSpace(request.EntityType) + "\x00" +
			strings.TrimSpace(request.EntityID) + "\x00" + stringValue(request.Locale)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, request)
	}
	return result
}
