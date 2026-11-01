package domain

type UsageDomain int32

const (
	UsageUnspecified UsageDomain = iota
	UsagePost
	UsagePage
	UsageWork
	UsageSiteSettings
	UsageRelease
	UsageTrack
	UsageArtist
	UsageLabel
	UsageClient
	UsageSeries
	UsageForm
	UsageProgramEvent
	UsageMapPlace
)

type UsageView struct {
	Domain                  UsageDomain
	EntityID, ReferencePath string
	BlockID                 *string
	Count                   int32
	BlockType, Title, Link  *string
}

type UsageDomainCount struct {
	Domain UsageDomain
	Count  int64
}

type DeletionImpact struct {
	FileID          string
	TotalUsageCount int64
	DomainCounts    []*UsageDomainCount
	FirstUsages     []*UsageView
	HasMoreUsages   bool
}
