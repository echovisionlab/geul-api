package series

import (
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// seriesAudits collects the records of one Series mutation while it runs;
// the mutation names them all once its change commits (mutation.Audits).
type seriesAudits struct {
	records []mutation.Audit
}

func (a *seriesAudits) audit() mutation.Audit {
	return mutation.Audits(a.records...)
}

func (a *seriesAudits) add(action sharedtelemetry.AuditAction, build func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error)) {
	a.records = append(a.records, mutation.RequestAudit(action, build))
}

func (a *seriesAudits) created(seriesID string) {
	a.add(sharedtelemetry.AuditPostSeriesCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesCreatedAuditRecord(metadata, seriesID)
	})
}

func (a *seriesAudits) deleted(seriesID string) {
	a.add(sharedtelemetry.AuditPostSeriesDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesDeletedAuditRecord(metadata, seriesID)
	})
}

func (a *seriesAudits) sourceMetadata(seriesID string, fields []string) {
	if len(fields) == 0 {
		return
	}
	a.add(sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesSourceMetadataAuditRecord(metadata, seriesID, fields)
	})
}

func (a *seriesAudits) lifecycle(seriesID string, previous, next sharedtelemetry.AuditState) {
	a.add(sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesLifecycleAuditRecord(metadata, seriesID, previous, next)
	})
}

func (a *seriesAudits) manager(seriesID, memberID string, previous, next sharedtelemetry.AuditRelationship) {
	a.add(sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesManagerAuditRecord(metadata, seriesID, memberID, previous, next)
	})
}

func (a *seriesAudits) membership(seriesID, postID, previousSeriesID, nextSeriesID string) {
	a.add(sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesMembershipAuditRecord(metadata, seriesID, postID, previousSeriesID, nextSeriesID)
	})
}

func (a *seriesAudits) order(seriesID string, postIDs []string) {
	a.add(sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesOrderAuditRecord(metadata, seriesID, postIDs)
	})
}

func (a *seriesAudits) featuredImage(seriesID string, operation sharedtelemetry.AuditCollectionOperation, fileID string) {
	a.add(sharedtelemetry.AuditPostSeriesUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPostSeriesFeaturedImageAuditRecord(metadata, seriesID, operation, fileID)
	})
}

func postSeriesAuditState(status Status) sharedtelemetry.AuditState {
	if status == StatusPublished {
		return sharedtelemetry.AuditStatePublished
	}
	return sharedtelemetry.AuditStateDraft
}
