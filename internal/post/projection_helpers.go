package post

import (
	"context"
	"fmt"
	"strings"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
)

func (s *Service) loadReadyManageOgAssetRefs(ctx context.Context, candidateIDs ...*string) (map[string]*mediaasset.Reference, error) {
	assetIDs := normalizedManageOgAssetIDs(candidateIDs)
	if len(assetIDs) == 0 {
		return make(map[string]*mediaasset.Reference), nil
	}
	return readStore(ctx, s, func(store Store) (map[string]*mediaasset.Reference, error) {
		return store.ReadyOgAssets(ctx, assetIDs)
	})
}

func manageOgAssetFromReadyMap(ready map[string]*mediaasset.Reference, candidateIDs ...*string) *mediaasset.Reference {
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		if assetID := strings.TrimSpace(*candidateID); assetID != "" && ready[assetID] != nil {
			return ready[assetID]
		}
	}
	return nil
}

func (s *Service) readyManageOgAssetRef(ctx context.Context, candidateIDs ...*string) (*mediaasset.Reference, error) {
	ready, err := s.loadReadyManageOgAssetRefs(ctx, candidateIDs...)
	if err != nil {
		return nil, err
	}
	return manageOgAssetFromReadyMap(ready, candidateIDs...), nil
}

func normalizedManageOgAssetIDs(candidateIDs []*string) []string {
	seen := make(map[string]struct{}, len(candidateIDs))
	assetIDs := make([]string, 0, len(candidateIDs))
	for _, candidateID := range candidateIDs {
		if candidateID == nil {
			continue
		}
		assetID := strings.TrimSpace(*candidateID)
		if assetID == "" {
			continue
		}
		if _, ok := seen[assetID]; ok {
			continue
		}
		seen[assetID] = struct{}{}
		assetIDs = append(assetIDs, assetID)
	}
	return assetIDs
}

func (s *Service) resolveVersionContributorNames(ctx context.Context, contributorLists ...[]string) (map[string]string, error) {
	allIDs := make([]string, 0)
	for _, contributorIDs := range contributorLists {
		allIDs = append(allIDs, contributorIDs...)
	}
	normalized := normalizeContributorMemberIDs(allIDs)
	if len(normalized) == 0 {
		return map[string]string{}, nil
	}
	names, err := readStore(ctx, s, func(store Store) (map[string]string, error) {
		return store.MemberNicknames(ctx, normalized)
	})
	if err != nil {
		return nil, fmt.Errorf("resolve post version contributor Members: %w", err)
	}
	if len(names) != len(normalized) {
		return nil, fmt.Errorf("post version contributor Member set is incomplete")
	}
	return names, nil
}

func versionContributors(contributorIDs []string, names map[string]string) ([]VersionContributor, error) {
	normalized := normalizeContributorMemberIDs(contributorIDs)
	contributors := make([]VersionContributor, 0, len(normalized))
	for _, memberID := range normalized {
		nickname := strings.TrimSpace(names[memberID])
		if nickname == "" {
			return nil, fmt.Errorf("post version contributor Member %s is unresolved", memberID)
		}
		contributors = append(contributors, VersionContributor{MemberID: memberID, Nickname: nickname})
	}
	return contributors, nil
}
