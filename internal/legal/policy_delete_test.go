package legal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPolicyDeletionCancelsOutstandingDeliveryAndPreservesSealedHistory(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		t.Run(kind, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
			require.NoError(t, err)
			for _, sql := range []string{
				`CREATE TABLE email_delivery_run (id TEXT PRIMARY KEY,terms_id TEXT,privacy_id TEXT,status TEXT,definition_sealed BOOLEAN,render_snapshot TEXT,completed_at DATETIME,updated_at DATETIME,skipped_count INTEGER)`,
				`CREATE TABLE email_delivery_recipient (id TEXT PRIMARY KEY,run_id TEXT,status TEXT,error_type TEXT,terminal_at DATETIME,delivery_claim_id TEXT,delivery_claim_expires_at DATETIME,updated_at DATETIME)`,
			} {
				require.NoError(t, db.Exec(sql).Error)
			}
			statuses := []string{"scheduled", "sending", "sent", "failed", "skipped", "cancelled"}
			for _, status := range statuses {
				require.NoError(t, db.Exec("INSERT INTO email_delivery_run (id,"+kind+"_id,status,definition_sealed,render_snapshot,skipped_count) VALUES (?,?,?,true,?,0)", status, "policy", status, `{"sealed":"keep"}`).Error)
				if status == "scheduled" || status == "sending" {
					require.NoError(t, db.Exec(`INSERT INTO email_delivery_recipient (id,run_id,status,delivery_claim_id,delivery_claim_expires_at) VALUES (?,?,'pending','claimed',?)`, status, status, time.Now().Add(time.Hour)).Error)
				}
			}
			require.NoError(t, db.Exec("INSERT INTO email_delivery_run (id,"+kind+"_id,status,definition_sealed,render_snapshot,skipped_count) VALUES ('unrelated','other','sending',true,?,0)", `{"sealed":"keep"}`).Error)
			require.NoError(t, db.Exec(`INSERT INTO email_delivery_recipient (id,run_id,status) VALUES ('already-sent','sending','sent')`).Error)
			// Retrying the cleanup must not double-count skipped recipients.
			for range 2 {
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					return cancelLegalNoticeDeliveryForPolicyDeletion(t.Context(), tx, kind, "policy")
				}))
			}
			var runs []model.CampaignDeliveryRun
			require.NoError(t, db.Order("id").Find(&runs).Error)
			require.Len(t, runs, 7)
			for _, run := range runs {
				require.True(t, run.DefinitionSealed)
				require.Equal(t, model.JSONFields{"sealed": "keep"}, run.RenderSnapshot)
				if run.ID == "scheduled" || run.ID == "sending" {
					require.Equal(t, "cancelled", run.Status)
					require.Equal(t, 1, run.SkippedCount)
					require.NotNil(t, run.CompletedAt)
				} else if run.ID == "unrelated" {
					require.Equal(t, "sending", run.Status)
				} else {
					require.Equal(t, run.ID, run.Status)
				}
			}
			var recipients []model.CampaignDeliveryRecipient
			require.NoError(t, db.Find(&recipients).Error)
			require.Len(t, recipients, 3)
			for _, recipient := range recipients {
				if recipient.ID == "already-sent" {
					require.Equal(t, "sent", recipient.Status)
					continue
				}
				require.Equal(t, "skipped", recipient.Status)
				require.Equal(t, "policy_deleted", *recipient.ErrorType)
				require.Nil(t, recipient.DeliveryClaimID)
				require.Nil(t, recipient.DeliveryClaimExpiresAt)
				require.NotNil(t, recipient.TerminalAt)
			}
		})
	}
}

func TestPolicyDeletionDeliveryCleanupRollsBackWithTransaction(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE email_delivery_run (id TEXT PRIMARY KEY,terms_id TEXT,status TEXT,completed_at DATETIME,updated_at DATETIME,skipped_count INTEGER)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE email_delivery_recipient (id TEXT PRIMARY KEY,run_id TEXT,status TEXT,error_type TEXT,terminal_at DATETIME,delivery_claim_id TEXT,delivery_claim_expires_at DATETIME,updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_run (id,terms_id,status,skipped_count) VALUES ('run','policy','sending',0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO email_delivery_recipient (id,run_id,status) VALUES ('recipient','run','pending')`).Error)
	failure := errors.New("later policy deletion failed")
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := cancelLegalNoticeDeliveryForPolicyDeletion(t.Context(), tx, "terms", "policy"); err != nil {
			return err
		}
		return failure
	})
	require.ErrorIs(t, err, failure)
	var run model.CampaignDeliveryRun
	require.NoError(t, db.First(&run, "id = ?", "run").Error)
	require.Equal(t, "sending", run.Status)
	require.Zero(t, run.SkippedCount)
	var recipient model.CampaignDeliveryRecipient
	require.NoError(t, db.First(&recipient, "id = ?", "recipient").Error)
	require.Equal(t, "pending", recipient.Status)
}

type policyDeletionOG struct {
	OG
	current   *CurrentRoute
	released  bool
	requested string
	err       error
}

func (og *policyDeletionOG) CurrentForRoute(context.Context, *gorm.DB, string) (*CurrentRoute, error) {
	return og.current, og.err
}
func (*policyDeletionOG) RouteID(string) string { return "public-route" }
func (og *policyDeletionOG) CancelAndRelease(_ context.Context, _ *gorm.DB, kind, id string) error {
	og.released = id == "public-route"
	return nil
}
func (og *policyDeletionOG) RequestSaved(_ context.Context, _ *gorm.DB, kind, id, locale string, all bool, reason string) error {
	og.requested = id
	return nil
}

func TestPolicyDeletionRefreshesOnlyTheAffectedPublicRoute(t *testing.T) {
	for _, kind := range []string{"terms", "privacy"} {
		before := &CurrentRoute{ID: "deleted"}
		og := &policyDeletionOG{current: &CurrentRoute{ID: "fallback"}}
		require.NoError(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", before))
		require.Equal(t, "fallback", og.requested)
		require.False(t, og.released)
		og = &policyDeletionOG{}
		require.NoError(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", before))
		require.True(t, og.released)
		og = &policyDeletionOG{err: errors.New("lookup failed")}
		require.Error(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", before))
		require.False(t, og.released)
		require.NoError(t, refreshLegalRouteAfterPolicyDeletion(t.Context(), nil, og, kind, "deleted", &CurrentRoute{ID: "other"}))
	}
}
