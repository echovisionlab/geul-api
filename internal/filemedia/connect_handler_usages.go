package filemedia

import managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"

func fileUsageViewsProto(values []*UsageView) []*managev1.FileUsage {
	if values == nil {
		return nil
	}
	result := make([]*managev1.FileUsage, len(values))
	for i, v := range values {
		if v == nil {
			continue
		}
		result[i] = &managev1.FileUsage{Domain: managev1.FileUsageDomain(v.Domain), EntityId: v.EntityID, ReferencePath: v.ReferencePath, BlockId: v.BlockID, Count: v.Count, BlockType: v.BlockType, Title: v.Title, Link: v.Link}
	}
	return result
}
func deletionImpactsProto(values []*DeletionImpact) []*managev1.FileDeletionImpact {
	if values == nil {
		return nil
	}
	result := make([]*managev1.FileDeletionImpact, len(values))
	for i, v := range values {
		if v == nil {
			continue
		}
		counts := make([]*managev1.FileUsageDomainCount, len(v.DomainCounts))
		for j, count := range v.DomainCounts {
			if count != nil {
				counts[j] = &managev1.FileUsageDomainCount{Domain: managev1.FileUsageDomain(count.Domain), Count: count.Count}
			}
		}
		result[i] = &managev1.FileDeletionImpact{FileId: v.FileID, TotalUsageCount: v.TotalUsageCount, DomainCounts: counts, FirstUsages: fileUsageViewsProto(v.FirstUsages), HasMoreUsages: v.HasMoreUsages}
	}
	return result
}
