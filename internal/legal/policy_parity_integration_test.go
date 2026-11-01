//go:build integration

package legal_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/email"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// legalParityPolicy is the kind-neutral view of a Terms or Privacy response.
type legalParityPolicy struct {
	ID       string
	Revision string
	Version  int32
	Title    string
	Status   string
}

// legalParityOps runs one public or collaboration operation of one policy
// kind, so the same scenario drives both services.
type legalParityOps struct {
	create            func(context.Context, *string) (legalParityPolicy, error)
	get               func(context.Context, string) (legalParityPolicy, error)
	updateSourceTitle func(context.Context, string, string, string, string) (string, error)
	schedule          func(context.Context, string, string, time.Time) (legalParityPolicy, error)
	cancel            func(context.Context, string) (legalParityPolicy, error)
	activate          func(context.Context, string, string) (legalParityPolicy, error)
	delete            func(context.Context, string, string) error
}

type legalParityAuditRow struct {
	ActorKind     string `gorm:"column:actor_kind"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	Action        string `gorm:"column:action"`
	TargetType    string `gorm:"column:target_type"`
	Attributes    string `gorm:"column:attributes"`
}

type legalParityDeliveryRun struct {
	EventKey         string
	Status           string
	RunKind          string
	TemplateDataKeys []string
}

// TestLegalPolicyTermsPrivacyParityIntegration pins what the Terms and
// Privacy services do for the same operations so the two can be merged into
// one implementation. Each table field that differs between the rows is a
// current divergence the merge must keep or deliberately change.
func TestLegalPolicyTermsPrivacyParityIntegration(t *testing.T) {
	t.Parallel()
	for _, kind := range []struct {
		name           string
		status         func(string) string
		shareLinkType  string
		updateEvent    string
		effectiveEvent string
		effectiveURL   string
		ops            func(legalIntegration) legalParityOps

		// Differences between the services.
		defaultTitle       string // "Terms of Service" vs "Privacy Policy"
		notFoundResource   string // Get/Schedule/Cancel/Delete: "terms" vs "privacy policy"
		scheduleNonDraft   string // shared errs constant vs a privacy-specific literal
		cancelNonScheduled string // shared errs constant vs a privacy-specific literal
	}{
		{
			name:               "terms",
			status:             func(state string) string { return "TERMS_STATUS_" + state },
			shareLinkType:      managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(),
			updateEvent:        email.EventTermsUpdate.String(),
			effectiveEvent:     email.EventTermsEffective.String(),
			effectiveURL:       "terms_url",
			ops:                termsParityOps,
			defaultTitle:       "Terms of Service",
			notFoundResource:   "terms",
			scheduleNonDraft:   "can only schedule draft terms",
			cancelNonScheduled: "can only cancel scheduled terms",
		},
		{
			name:               "privacy",
			status:             func(state string) string { return "PRIVACY_STATUS_" + state },
			shareLinkType:      managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
			updateEvent:        email.EventPrivacyUpdate.String(),
			effectiveEvent:     email.EventPrivacyEffective.String(),
			effectiveURL:       "privacy_url",
			ops:                privacyParityOps,
			defaultTitle:       "Privacy Policy",
			notFoundResource:   "privacy policy",
			scheduleNonDraft:   "can only schedule draft privacy policies",
			cancelNonScheduled: "can only cancel scheduled privacy policies",
		},
	} {
		t.Run(kind.name, func(t *testing.T) {
			// The schema seeds the active notice template of every legal event.
			db := newLegalLocalKratosIntegrationDB(t)
			identityID := testutil.IntegrationUUID()
			memberID := testutil.SeedPostIntegrationIdentity(t, db, identityID, "Legal parity admin")
			spiceDB := testutil.IntegrationSpiceDB(t)
			testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
			ctx := testutil.NewAuditContext(t, identityID, memberID)
			ops := kind.ops(newLegalIntegration(t, db, spiceDB, apitelemetry.NewDurableWriter(db)))
			manage := policyv1.TermsHistory.Manage
			if kind.name == "privacy" {
				manage = policyv1.PrivacyHistory.Manage
			}

			created, err := ops.create(ctx, nil)
			require.NoError(t, err)
			require.Equal(t, kind.defaultTitle, created.Title)
			require.Equal(t, kind.status("DRAFT"), created.Status)
			require.Positive(t, created.Version)
			testutil.RequireSynchronousResourceAuthorization(t, spiceDB, manage, created.ID, identityID, true)
			titled, err := ops.create(ctx, ptrString("Explicit "+kind.name+" title"))
			require.NoError(t, err)
			require.Equal(t, "Explicit "+kind.name+" title", titled.Title)
			require.Equal(t, created.Version+1, titled.Version)
			loaded, err := ops.get(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, created, loaded)

			revision, err := ops.updateSourceTitle(ctx, created.ID, created.Revision, "Updated "+kind.name+" title", memberID)
			require.NoError(t, err)
			require.NotEqual(t, created.Revision, revision)
			loaded, err = ops.get(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, "Updated "+kind.name+" title", loaded.Title)
			require.Equal(t, revision, loaded.Revision)

			effectiveFrom := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Microsecond)
			scheduled, err := ops.schedule(ctx, created.ID, revision, effectiveFrom)
			require.NoError(t, err)
			require.Equal(t, kind.status("SCHEDULED"), scheduled.Status)
			require.Equal(t, revision, scheduled.Revision)
			require.Equal(t, []legalParityDeliveryRun{{
				EventKey: kind.updateEvent, Status: legaldomain.CampaignDeliveryRunStatusScheduled, RunKind: "legal_notice",
				TemplateDataKeys: []string{"effective_date", "policy_title", "preview_url"},
			}}, legalParityDeliveryRuns(t, db, kind.name, created.ID))
			// The automatic preview link is materialized at dispatch, not here.
			requireRelationWhereCount(t, db, "share_link", "entity_type = ? AND entity_id = ?", 0, kind.shareLinkType, created.ID)
			_, err = ops.schedule(ctx, created.ID, revision, effectiveFrom)
			requireLegalParityError(t, err, connect.CodeFailedPrecondition, kind.scheduleNonDraft)

			cancelled, err := ops.cancel(ctx, created.ID)
			require.NoError(t, err)
			require.Equal(t, kind.status("DRAFT"), cancelled.Status)
			require.Equal(t, revision, cancelled.Revision)
			require.Equal(t, []legalParityDeliveryRun{{
				EventKey: kind.updateEvent, Status: legaldomain.CampaignDeliveryRunStatusCancelled, RunKind: "legal_notice",
				TemplateDataKeys: []string{"effective_date", "policy_title", "preview_url"},
			}}, legalParityDeliveryRuns(t, db, kind.name, created.ID))
			requireRelationWhereCount(t, db, "share_link", "entity_type = ? AND entity_id = ?", 0, kind.shareLinkType, created.ID)
			_, err = ops.cancel(ctx, created.ID)
			requireLegalParityError(t, err, connect.CodeFailedPrecondition, kind.cancelNonScheduled)

			activated, err := ops.activate(ctx, created.ID, revision)
			require.NoError(t, err)
			require.Equal(t, kind.status("ACTIVE"), activated.Status)
			runs := legalParityDeliveryRuns(t, db, kind.name, created.ID)
			require.Len(t, runs, 2)
			require.Equal(t, legalParityDeliveryRun{
				EventKey: kind.effectiveEvent, Status: legaldomain.CampaignDeliveryRunStatusScheduled, RunKind: "legal_notice",
				TemplateDataKeys: []string{kind.effectiveURL},
			}, runs[1])

			// Cancelling the only scheduled version cancels its generations
			// instead of requesting a schedule_cancelled render.
			require.Equal(t, []string{
				kind.name + "_scheduled/cancelled/20", kind.name + "_activated/queued/20",
			}, legalParityOGReasons(t, db, kind.name))
			require.NoError(t, ops.delete(ctx, created.ID, revision))
			require.Equal(t, []string{
				kind.name + "_scheduled/cancelled/20", kind.name + "_activated/cancelled/20",
			}, legalParityOGReasons(t, db, kind.name), "deleting the only active version cancels its route render")
			requireRelationWhereCount(t, db, kind.name+"_history", "id = ?", 0, created.ID)
			testutil.RequireSynchronousResourceAuthorization(t, spiceDB, manage, created.ID, identityID, false)
			runs = legalParityDeliveryRuns(t, db, kind.name, created.ID)
			require.Len(t, runs, 2, "delivery history outlives the deleted policy")
			require.Equal(t, legaldomain.CampaignDeliveryRunStatusCancelled, runs[1].Status)

			audits := legalParityAuditRows(t, db, created.ID)
			require.Equal(t, []string{
				"legal_policy.created", "legal_policy.updated", "legal_policy.updated",
				"legal_policy.updated", "legal_policy.deleted",
			}, legalParityAuditActions(audits), "a source title edit appends no Domain Audit")
			for _, row := range audits {
				require.Equal(t, "member", row.ActorKind)
				require.Equal(t, memberID, row.ActorMemberID)
				require.Equal(t, "legal_policy", row.TargetType)
				require.Equal(t, kind.name, legalParityAuditAttributes(t, row)["policy_type"])
			}
			require.Equal(t, map[string]any{
				"policy_type": kind.name, "version_number": float64(created.Version),
			}, legalParityAuditAttributes(t, audits[0]))
			require.Equal(t, map[string]any{
				"policy_type": kind.name, "version_number": float64(created.Version),
				"changed_fields": []any{"effective_at", "status"},
				"previous_state": "draft", "new_state": "scheduled",
				"effective_at": effectiveFrom.Format(time.RFC3339Nano),
			}, legalParityAuditAttributes(t, audits[1]))
			require.Equal(t, map[string]any{
				"policy_type": kind.name, "version_number": float64(created.Version),
				"changed_fields": []any{"effective_at", "status"},
				"previous_state": "scheduled", "new_state": "draft",
				"effective_at": effectiveFrom.Format(time.RFC3339Nano),
			}, legalParityAuditAttributes(t, audits[2]))
			activation := legalParityAuditAttributes(t, audits[3])
			require.NotEmpty(t, activation["effective_at"])
			delete(activation, "effective_at")
			require.Equal(t, map[string]any{
				"policy_type": kind.name, "version_number": float64(created.Version),
				"changed_fields": []any{"effective_at", "status"},
				"previous_state": "draft", "new_state": "active",
			}, activation)
			require.Equal(t, map[string]any{
				"policy_type": kind.name, "version_number": float64(created.Version),
			}, legalParityAuditAttributes(t, audits[4]))

			missingID := uuid.NewString()
			_, err = ops.get(ctx, missingID)
			requireLegalParityError(t, err, connect.CodeNotFound, kind.notFoundResource+" not found: "+missingID)
			_, err = ops.schedule(ctx, missingID, revision, effectiveFrom)
			requireLegalParityError(t, err, connect.CodeNotFound, kind.notFoundResource+" not found: "+missingID)
			_, err = ops.cancel(ctx, missingID)
			requireLegalParityError(t, err, connect.CodeNotFound, kind.notFoundResource+" not found: "+missingID)
			_, err = ops.activate(ctx, missingID, revision)
			requireLegalParityError(t, err, connect.CodeNotFound, kind.name+" not found: "+missingID)
			err = ops.delete(ctx, missingID, revision)
			requireLegalParityError(t, err, connect.CodeNotFound, kind.notFoundResource+" not found: "+missingID)
		})
	}
}

func termsParityOps(integration legalIntegration) legalParityOps {
	service, internal := integration.terms(), integration.internalTerms()
	fromTerms := func(terms *managev1.Terms) legalParityPolicy {
		return legalParityPolicy{
			ID: terms.Id, Revision: terms.Revision, Version: terms.Version, Title: terms.Title, Status: terms.Status.String(),
		}
	}
	fromLifecycle := func(response *managev1.TermsLifecycleMutationResponse) legalParityPolicy {
		return legalParityPolicy{ID: response.Id, Revision: response.DocumentRevision, Status: response.Status.String()}
	}
	return legalParityOps{
		create: func(ctx context.Context, title *string) (legalParityPolicy, error) {
			response, err := service.CreateTermsVersion(ctx, connect.NewRequest(&managev1.CreateTermsVersionRequest{
				Title: title, Document: legalPolicyDocumentFixture("en", "Parity terms"),
			}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromTerms(response.Msg), nil
		},
		get: func(ctx context.Context, id string) (legalParityPolicy, error) {
			response, err := service.GetTermsVersion(ctx, connect.NewRequest(&managev1.GetTermsVersionRequest{Id: id}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromTerms(response.Msg), nil
		},
		updateSourceTitle: func(ctx context.Context, id, revision, title, contributor string) (string, error) {
			response, err := internal.UpdateTermsLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateTermsLocaleMetadataRequest{
				TermsId: id, Locale: "en", Title: &title, ExpectedRevision: revision, ContributorMemberIds: []string{contributor},
			}))
			if err != nil {
				return "", err
			}
			return response.Msg.DocumentRevision, nil
		},
		schedule: func(ctx context.Context, id, revision string, at time.Time) (legalParityPolicy, error) {
			response, err := service.ScheduleTerms(ctx, connect.NewRequest(&managev1.ScheduleTermsRequest{
				Id: id, ExpectedRevision: revision, EffectiveFrom: timestamppb.New(at),
			}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromLifecycle(response.Msg), nil
		},
		cancel: func(ctx context.Context, id string) (legalParityPolicy, error) {
			response, err := service.CancelTermsSchedule(ctx, connect.NewRequest(&managev1.CancelTermsScheduleRequest{Id: id}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromLifecycle(response.Msg), nil
		},
		activate: func(ctx context.Context, id, revision string) (legalParityPolicy, error) {
			response, err := service.ActivateTermsNow(ctx, connect.NewRequest(&managev1.ActivateTermsNowRequest{
				Id: id, ExpectedRevision: revision,
			}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromLifecycle(response.Msg), nil
		},
		delete: func(ctx context.Context, id, revision string) error {
			_, err := service.DeleteTerms(ctx, connect.NewRequest(&managev1.DeleteTermsRequest{Id: id, ExpectedRevision: revision}))
			return err
		},
	}
}

func privacyParityOps(integration legalIntegration) legalParityOps {
	service, internal := integration.privacy(), integration.internalPrivacy()
	fromPrivacy := func(privacy *managev1.Privacy) legalParityPolicy {
		return legalParityPolicy{
			ID: privacy.Id, Revision: privacy.Revision, Version: privacy.Version, Title: privacy.Title, Status: privacy.Status.String(),
		}
	}
	fromLifecycle := func(response *managev1.PrivacyLifecycleMutationResponse) legalParityPolicy {
		return legalParityPolicy{ID: response.Id, Revision: response.DocumentRevision, Status: response.Status.String()}
	}
	return legalParityOps{
		create: func(ctx context.Context, title *string) (legalParityPolicy, error) {
			response, err := service.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
				Title: title, Document: legalPolicyDocumentFixture("en", "Parity privacy"),
			}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromPrivacy(response.Msg), nil
		},
		get: func(ctx context.Context, id string) (legalParityPolicy, error) {
			response, err := service.GetPrivacyVersion(ctx, connect.NewRequest(&managev1.GetPrivacyVersionRequest{Id: id}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromPrivacy(response.Msg), nil
		},
		updateSourceTitle: func(ctx context.Context, id, revision, title, contributor string) (string, error) {
			response, err := internal.UpdatePrivacyLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdatePrivacyLocaleMetadataRequest{
				PrivacyId: id, Locale: "en", Title: &title, ExpectedRevision: revision, ContributorMemberIds: []string{contributor},
			}))
			if err != nil {
				return "", err
			}
			return response.Msg.DocumentRevision, nil
		},
		schedule: func(ctx context.Context, id, revision string, at time.Time) (legalParityPolicy, error) {
			response, err := service.SchedulePrivacy(ctx, connect.NewRequest(&managev1.SchedulePrivacyRequest{
				Id: id, ExpectedRevision: revision, EffectiveFrom: timestamppb.New(at),
			}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromLifecycle(response.Msg), nil
		},
		cancel: func(ctx context.Context, id string) (legalParityPolicy, error) {
			response, err := service.CancelPrivacySchedule(ctx, connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{Id: id}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromLifecycle(response.Msg), nil
		},
		activate: func(ctx context.Context, id, revision string) (legalParityPolicy, error) {
			response, err := service.ActivatePrivacyNow(ctx, connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
				Id: id, ExpectedRevision: revision,
			}))
			if err != nil {
				return legalParityPolicy{}, err
			}
			return fromLifecycle(response.Msg), nil
		},
		delete: func(ctx context.Context, id, revision string) error {
			_, err := service.DeletePrivacy(ctx, connect.NewRequest(&managev1.DeletePrivacyRequest{Id: id, ExpectedRevision: revision}))
			return err
		},
	}
}

func requireLegalParityError(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, code, connectErr.Code(), connectErr.Message())
	require.Equal(t, message, connectErr.Message())
}

func legalParityDeliveryRuns(t *testing.T, db *gorm.DB, kind, id string) []legalParityDeliveryRun {
	t.Helper()
	var rows []struct {
		TemplateEventKey string `gorm:"column:template_event_key"`
		Status           string `gorm:"column:status"`
		RunKind          string `gorm:"column:run_kind"`
		TemplateData     string `gorm:"column:template_data"`
	}
	require.NoError(t, db.Raw(
		`SELECT template_event_key, status, run_kind, template_data::text AS template_data
		FROM email_delivery_run WHERE `+kind+`_id = ? ORDER BY created_at, id`, id,
	).Scan(&rows).Error)
	runs := make([]legalParityDeliveryRun, 0, len(rows))
	for _, row := range rows {
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(row.TemplateData), &data))
		keys := make([]string, 0, len(data))
		for key := range data {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		runs = append(runs, legalParityDeliveryRun{
			EventKey: row.TemplateEventKey, Status: row.Status, RunKind: row.RunKind, TemplateDataKeys: keys,
		})
	}
	return runs
}

func legalParityAuditRows(t *testing.T, db *gorm.DB, targetID string) []legalParityAuditRow {
	t.Helper()
	var rows []legalParityAuditRow
	require.NoError(t, db.Raw(
		`SELECT actor_kind, COALESCE(actor_member_id::text, '') AS actor_member_id, action, target_type,
			attributes::text AS attributes
		FROM domain_audit WHERE target_id = ? ORDER BY occurred_at, audit_id`, targetID,
	).Scan(&rows).Error)
	return rows
}

func legalParityAuditActions(rows []legalParityAuditRow) []string {
	actions := make([]string, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, row.Action)
	}
	return actions
}

func legalParityAuditAttributes(t *testing.T, row legalParityAuditRow) map[string]any {
	t.Helper()
	var attributes map[string]any
	require.NoError(t, json.Unmarshal([]byte(row.Attributes), &attributes))
	return attributes
}

// legalParityOGReasons summarizes the OG generations of the canonical policy
// route, which every version of one kind shares, as reason/status/locales.
func legalParityOGReasons(t *testing.T, db *gorm.DB, kind string) []string {
	t.Helper()
	var reasons []string
	require.NoError(t, db.Raw(
		`SELECT run.reason || '/' || generation.status || '/' || COUNT(*) FROM og_generation generation
		JOIN og_generation_run run ON run.id = generation.run_id
		JOIN og_generation_target target ON target.id = generation.target_id
		WHERE target.entity_type = ? AND target.entity_id = ?
		GROUP BY run.reason, generation.status
		ORDER BY MIN(generation.request_sequence)`, kind, legaladapter.RouteID(kind),
	).Scan(&reasons).Error)
	return reasons
}
