package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type requestTimeAuthorityTestDomains struct {
	testTranslationDomains
	applyCalls int
	ogCalls    int
	callOrder  []string
	rootErr    error
	result     AppliedTranslationTarget
}

func (d *requestTimeAuthorityTestDomains) RequestLocaleOG(
	context.Context,
	*gorm.DB,
	*og.Planner,
	*og.Refresher,
	string,
	string,
	string,
	string,
) (bool, error) {
	d.callOrder = append(d.callOrder, "og")
	d.ogCalls++
	return false, nil
}

func (d *requestTimeAuthorityTestDomains) LockRoot(context.Context, *gorm.DB, string, string) error {
	d.callOrder = append(d.callOrder, "root")
	return d.rootErr
}

func (d *requestTimeAuthorityTestDomains) RequireEditable(
	context.Context,
	*gorm.DB,
	string,
	string,
) error {
	return errors.New("delivery must not recheck current lifecycle or authority")
}

func (d *requestTimeAuthorityTestDomains) BuildCandidate(
	*translation.ExtractionPlan,
	*translation.SourceDocument,
	map[string]translation.UnitResult,
) (*translation.Candidate, error) {
	return &translation.Candidate{}, nil
}

func (d *requestTimeAuthorityTestDomains) ApplyCandidate(
	context.Context,
	*gorm.DB,
	*contentblock.Store,
	*model.TranslationJob,
	*translation.Candidate,
	translation.EntryWrite,
) (AppliedTranslationTarget, error) {
	d.callOrder = append(d.callOrder, "apply")
	d.applyCalls++
	return d.result, nil
}

func TestTranslationApplyUsesAcceptedRequestAuthorityAfterRequesterDisappears(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_170, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "request-time-authority", uuid.NewString())
	requestedBy := uuid.NewString()
	job.RequestedByMemberID = requestedBy
	require.NoError(t, db.Model(&model.TranslationJob{}).
		Where("id = ?", job.ID).
		Update("requested_by_member_id", requestedBy).Error)
	domains := &requestTimeAuthorityTestDomains{}
	publisher := &appliedContentUpdatePublisher{}
	manager := &TranslationJobManager{
		db:        db,
		publisher: publisher,
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
		domains:   domains,
	}

	err := manager.applyTranslationDelivery(context.Background(), &translationDeliveryExecution{
		job:       job,
		startedAt: now,
		sourceDoc: &translation.SourceDocument{},
		plan:      &translation.ExtractionPlan{},
		response:  &translation.ProviderResponse{},
		generator: namedStubTranslationGenerator{providerName: "provider", modelName: "model"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, domains.applyCalls)
	require.Equal(t, []string{"root", "apply"}, domains.callOrder)
	require.Zero(t, domains.ogCalls)
	require.Empty(t, publisher.events)

	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestTranslationApplyDoesNotMutateDeletedRootAndDeletesTransportJob(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_171, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "deleted-root", uuid.NewString())
	domains := &requestTimeAuthorityTestDomains{rootErr: gorm.ErrRecordNotFound}
	publisher := &appliedContentUpdatePublisher{}
	manager := &TranslationJobManager{
		db:        db,
		publisher: publisher,
		now:       func() time.Time { return now },
		metrics:   newTranslationMetrics(),
		domains:   domains,
	}

	err := manager.applyTranslationDelivery(context.Background(), &translationDeliveryExecution{
		job:       job,
		startedAt: now,
		sourceDoc: &translation.SourceDocument{},
		plan:      &translation.ExtractionPlan{},
		response:  &translation.ProviderResponse{},
		generator: namedStubTranslationGenerator{providerName: "provider", modelName: "model"},
	})
	require.Error(t, err)
	require.Zero(t, domains.applyCalls)
	require.Equal(t, []string{"root"}, domains.callOrder)
	require.Empty(t, publisher.events)
	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestTranslationApplySkipsAlreadyCancelledJobWithoutSignal(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "cancelled", uuid.NewString())
	require.NoError(t, db.Delete(&model.TranslationJob{}, "id = ?", job.ID).Error)
	domains := &requestTimeAuthorityTestDomains{}
	publisher := &appliedContentUpdatePublisher{}
	manager := &TranslationJobManager{
		db: db, publisher: publisher, now: time.Now,
		metrics: newTranslationMetrics(), domains: domains,
	}

	applied, err := manager.applyAppliedTranslationResult(
		t.Context(),
		job,
		&translation.Candidate{},
		time.Now().UTC(),
	)
	require.NoError(t, err)
	require.False(t, applied)
	require.Zero(t, domains.applyCalls)
	require.Equal(t, []string{"root"}, domains.callOrder)
	require.Empty(t, publisher.events)
}

func TestTranslationApplyKeepsTransactionalInvalidationWhenLifecycleHintFails(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	now := time.Unix(1_700_000_172, 0).UTC()
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "lifecycle-failure", uuid.NewString())
	requestedBy := uuid.NewString()
	job.RequestedByMemberID = requestedBy
	require.NoError(t, db.Model(&model.TranslationJob{}).
		Where("id = ?", job.ID).
		Update("requested_by_member_id", requestedBy).Error)
	domains := &requestTimeAuthorityTestDomains{result: AppliedTranslationTarget{
		Changed: true, DocumentRevision: uuid.NewString(), TargetRevision: "tr1_exact",
	}}
	publisher := &appliedContentUpdatePublisher{lifecycleErr: errors.New("lifecycle notify failed")}
	manager := &TranslationJobManager{
		db: db, publisher: publisher, now: func() time.Time { return now },
		metrics: newTranslationMetrics(), domains: domains,
	}

	err := manager.applyTranslationDelivery(t.Context(), &translationDeliveryExecution{
		job:       job,
		startedAt: now,
		sourceDoc: &translation.SourceDocument{},
		plan:      &translation.ExtractionPlan{},
		response:  &translation.ProviderResponse{},
		generator: namedStubTranslationGenerator{providerName: "provider", modelName: "model"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, domains.ogCalls)
	require.Equal(t, []string{"root", "apply", "og"}, domains.callOrder)
	require.Len(t, publisher.events, 1)
	require.Equal(t, domains.result.DocumentRevision, publisher.events[0].GetDocumentRevision())
	require.Equal(t, domains.result.TargetRevision, publisher.events[0].GetTargetRevision())
	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}

func TestTranslationApplyCommitsDomainMutationAndDeletesTransportJob(t *testing.T) {
	db := newTranslationRetryTestDB(t)
	job := seedTranslationRetryTestJob(t, db, translationJobStatusRunning, "apply-terminal-delete", uuid.NewString())
	require.NoError(t, db.Exec(`CREATE TABLE applied_translation_result (job_id TEXT PRIMARY KEY)`).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO applied_translation_result (job_id) VALUES (?)`, job.ID).Error; err != nil {
			return err
		}
		return completeAppliedTranslationJob(context.Background(), tx, job.ID)
	}))
	var applied string
	require.NoError(t, db.Raw(`SELECT job_id FROM applied_translation_result WHERE job_id = ?`, job.ID).Scan(&applied).Error)
	require.Equal(t, job.ID, applied)
	require.ErrorIs(t, db.First(&model.TranslationJob{}, "id = ?", job.ID).Error, gorm.ErrRecordNotFound)
}
