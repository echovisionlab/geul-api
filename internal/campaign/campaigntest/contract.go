package campaigntest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type Harness struct {
	mutationtest.Harness[campaign.Store]
	QueueMessages func(*testing.T) []proto.Message
	SeedRecipient func(*testing.T, campaign.DeliveryRecipient) campaign.DeliveryRecipient
}

func FakeHarness(t *testing.T) Harness {
	t.Helper()
	s, _ := New()
	return Harness{Harness: mutationtest.FakeHarness[campaign.Store](s), QueueMessages: func(*testing.T) []proto.Message { return s.Queued() }, SeedRecipient: func(_ *testing.T, v campaign.DeliveryRecipient) campaign.DeliveryRecipient { return s.PutRecipient(v) }}
}
func RunStoreContract(t *testing.T, open func(*testing.T) Harness) {
	t.Helper()
	cases := map[string]func(*testing.T, mutationtest.Harness[campaign.Store]){}
	harnesses := map[*testing.T]Harness{}
	// RunContract opens and runs each case on the same goroutine. The map is
	// guarded because its subtests execute in parallel.
	var mu sync.Mutex
	for name, run := range storeContract {
		cases[name] = func(t *testing.T, _ mutationtest.Harness[campaign.Store]) {
			mu.Lock()
			h := harnesses[t]
			mu.Unlock()
			run(t, h)
		}
	}
	mutationtest.RunContract(t, func(t *testing.T) mutationtest.Harness[campaign.Store] {
		h := open(t)
		mu.Lock()
		harnesses[t] = h
		mu.Unlock()
		return h.Harness
	}, cases)
}
func transact(t *testing.T, h Harness, work func(context.Context, campaign.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h.Harness, work))
}
func read(t *testing.T, h Harness, work func(context.Context, campaign.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Read(t, h.Harness, work))
}
func at() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }
func insertCampaign(t *testing.T, h Harness) campaign.Campaign {
	t.Helper()
	now := at()
	v := campaign.Campaign{Name: "Contract", Subject: "Subject", SourceLocale: "en", Status: campaign.CampaignStatusDraft, TargetMode: campaign.CampaignTargetModeAll, RecipientScope: "SUBSCRIBED_USERS", CreatedAt: now, UpdatedAt: now}
	transact(t, h, func(ctx context.Context, s campaign.Store) error {
		doc, err := s.CreateContentDocument(ctx, contentblock.CreateInput{Profile: "email", SourceLocale: "en"})
		if err != nil {
			return err
		}
		id := doc.Document.ID.String()
		v.ContentDocumentID = &id
		if err := s.InsertCampaign(ctx, &v); err != nil {
			return err
		}
		_, err = s.InsertLocale(ctx, v.ID, "en", &v.Subject, now)
		return err
	})
	return v
}
func Target() campaign.CampaignDeliveryTarget {
	return campaign.CampaignDeliveryTarget{QueryVersion: campaign.CampaignDeliveryTargetQueryVersion, Mode: campaign.CampaignDeliveryTargetModeAllUsers, RecipientScope: "SUBSCRIBED_USERS"}
}
func Snapshot() campaign.CampaignDeliverySnapshot {
	return campaign.CampaignDeliverySnapshot{Subject: "Subject", ContentHTML: "<p>Content</p>", SourceLocale: "en", Translations: []campaign.CampaignDeliverySnapshotTranslation{{Locale: "en", Subject: "Subject", ContentHTML: "<p>Content</p>"}}}
}
func insertRun(t *testing.T, h Harness, c campaign.Campaign, status string) campaign.DeliveryRun {
	t.Helper()
	now := at()
	run := campaign.DeliveryRun{RunKind: campaign.EmailDeliveryRunKindCampaign, CampaignID: &c.ID, Status: status, ScheduledAt: now, TemplateData: map[string]any{}, RenderSnapshot: map[string]any{"subject": "Subject", "content_html": "<p>Content</p>", "source_locale": "en", "translations": []any{map[string]any{"locale": "en", "subject": "Subject", "content_html": "<p>Content</p>"}}}, SnapshotSchemaVersion: 1, SourceCampaignUpdatedAt: &c.UpdatedAt, TargetQueryVersion: campaign.CampaignDeliveryTargetQueryVersion, TargetMode: campaign.CampaignDeliveryTargetModeAllUsers, TargetRecipientScope: "SUBSCRIBED_USERS", CreatedAt: now, UpdatedAt: now}
	target := Target()
	transact(t, h, func(ctx context.Context, s campaign.Store) error { return s.InsertSealedRun(ctx, &run, &target) })
	return run
}
func recipient(t *testing.T, h Harness, runID string) campaign.DeliveryRecipient {
	t.Helper()
	return h.SeedRecipient(t, campaign.DeliveryRecipient{RunID: runID, RecipientEmail: uuid.NewString() + "@example.test", RecipientContextType: campaign.BulkEmailContextAccountCurrent, Status: "pending"})
}

var errRollback = errors.New("rollback contract transaction")
var storeContract = map[string]func(*testing.T, Harness){
	"campaign insert round trip and ordered list": func(t *testing.T, h Harness) {
		a := insertCampaign(t, h)
		b := insertCampaign(t, h)
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			got, err := s.FindCampaign(ctx, a.ID)
			require.NoError(t, err)
			require.Equal(t, a, got)
			rows, total, err := s.ListCampaigns(ctx, campaign.CampaignList{Limit: 1})
			require.NoError(t, err)
			require.EqualValues(t, 2, total)
			require.Len(t, rows, 1)
			require.Equal(t, b.ID, rows[0].ID)
			locales, err := s.CampaignSourceLocales(ctx, []string{a.ID, b.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]string{a.ID: "en", b.ID: "en"}, locales)
			return nil
		})
	},
	"missing campaign and delivery rows answer sentinels": func(t *testing.T, h Harness) {
		id := uuid.NewString()
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s campaign.Store) error { _, err := s.FindCampaign(ctx, id); return err }), campaign.ErrCampaignNotFound)
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error { _, err := s.LockCampaign(ctx, id); return err }), campaign.ErrCampaignNotFound)
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s campaign.Store) error { _, err := s.FindRun(ctx, id); return err }), campaign.ErrNotFound)
		require.ErrorIs(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s campaign.Store) error { _, err := s.FindRecipient(ctx, id); return err }), campaign.ErrNotFound)
	},
	"campaign updates and failed transaction rollback": func(t *testing.T, h Harness) {
		v := insertCampaign(t, h)
		name := "New name"
		next := at().Add(time.Second)
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			if err := s.UpdateCampaign(ctx, v, campaign.CampaignChange{Name: &name, UpdatedAt: next}); err != nil {
				return err
			}
			return errRollback
		}), errRollback)
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			got, err := s.FindCampaign(ctx, v.ID)
			require.NoError(t, err)
			require.Equal(t, v.Name, got.Name)
			return nil
		})
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			return s.UpdateCampaign(ctx, v, campaign.CampaignChange{Name: &name, UpdatedAt: next})
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			got, err := s.FindCampaign(ctx, v.ID)
			require.NoError(t, err)
			require.Equal(t, name, got.Name)
			require.Equal(t, next, got.UpdatedAt)
			return nil
		})
	},
	"durable run blocks campaign deletion": func(t *testing.T, h Harness) {
		v := insertCampaign(t, h)
		run := insertRun(t, h, v, "scheduled")
		require.True(t, run.DefinitionSealed)
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error { return s.DeleteCampaign(ctx, v) }), campaign.ErrDurableReference)
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			history, err := s.HasHistory(ctx, v.ID)
			require.NoError(t, err)
			require.True(t, history)
			return nil
		})
	},
	"sealed run target and scheduling order round trip": func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		run := insertRun(t, h, c, "scheduled")
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			got, err := s.FindRun(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, run, got)
			target, err := s.LoadDeliveryTarget(ctx, got)
			require.NoError(t, err)
			require.Equal(t, Target(), target)
			due, err := s.DueRuns(ctx, run.ScheduledAt, 25)
			require.NoError(t, err)
			require.Len(t, due, 1)
			require.Equal(t, run.ID, due[0].ID)
			future, err := s.DueRuns(ctx, run.ScheduledAt.Add(-time.Microsecond), 25)
			require.NoError(t, err)
			require.Empty(t, future)
			return nil
		})
	},
	"cancel refuses sending and only cancels scheduled": func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		scheduled := insertRun(t, h, c, "scheduled")
		done := insertRun(t, h, c, "failed")
		now := at()
		transact(t, h, func(ctx context.Context, s campaign.Store) error { return s.CancelActiveRuns(ctx, c.ID, now) })
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			v, err := s.FindRun(ctx, scheduled.ID)
			require.NoError(t, err)
			require.Equal(t, "cancelled", v.Status)
			v, err = s.FindRun(ctx, done.ID)
			require.NoError(t, err)
			require.Equal(t, "failed", v.Status)
			return nil
		})
		insertRun(t, h, c, "sending")
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error { return s.CancelActiveRuns(ctx, c.ID, now) }), campaign.ErrDeliveryStarted)
	},
	"claim compare and set fences live expired and stale owners": func(t *testing.T, h Harness) {
		run := insertRun(t, h, insertCampaign(t, h), "sending")
		r := recipient(t, h, run.ID)
		a, b := uuid.NewString(), uuid.NewString()
		now := at()
		expiry := now.Add(time.Minute)
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			claimed, err := s.ClaimRecipient(ctx, r, a, now, expiry)
			require.NoError(t, err)
			require.True(t, claimed)
			claimed, err = s.ClaimRecipient(ctx, r, b, now, expiry)
			require.NoError(t, err)
			require.False(t, claimed)
			return nil
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			rows, err := s.PendingRecipients(ctx, run.ID, "", 10, now)
			require.NoError(t, err)
			require.Empty(t, rows)
			pending, err := s.HasPendingRecipients(ctx, run.ID)
			require.NoError(t, err)
			require.True(t, pending)
			return nil
		})
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			claimed, err := s.ClaimRecipient(ctx, r, b, expiry, expiry.Add(time.Minute))
			require.NoError(t, err)
			require.True(t, claimed)
			return s.ReleaseRecipientClaim(ctx, r, a)
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			stored, err := s.FindRecipient(ctx, r.ID)
			require.NoError(t, err)
			require.Equal(t, &b, stored.DeliveryClaimID)
			return nil
		})
	},
	"result compare and set rejects stale tokens and terminal overwrites": func(t *testing.T, h Harness) {
		run := insertRun(t, h, insertCampaign(t, h), "sending")
		r := recipient(t, h, run.ID)
		claim := uuid.NewString()
		now := at()
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			_, err := s.ClaimRecipient(ctx, r, claim, now, now.Add(time.Minute))
			return err
		})
		result := campaign.RecipientResult{RecipientID: r.ID, Status: "sent", ProviderMessageID: "provider", RequireClaim: true, ClaimID: uuid.NewString()}
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			return s.StoreRecipientResult(ctx, result, run.ID, now)
		}), campaign.ErrNotFound)
		result.ClaimID = claim
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			return s.StoreRecipientResult(ctx, result, run.ID, now)
		})
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			return s.StoreRecipientResult(ctx, result, run.ID, now)
		}), campaign.ErrNotFound)
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			got, err := s.FindRecipient(ctx, r.ID)
			require.NoError(t, err)
			require.Equal(t, "sent", got.Status)
			require.Nil(t, got.DeliveryClaimID)
			require.Equal(t, &now, got.TerminalAt)
			return nil
		})
	},
	"recipient ordering counts and provider message lookup": func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		run := insertRun(t, h, c, "sending")
		a := recipient(t, h, run.ID)
		b := recipient(t, h, run.ID)
		now := at()
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			return s.StoreRecipientResult(ctx, campaign.RecipientResult{RecipientID: a.ID, Status: "sent", ProviderMessageID: "provider"}, run.ID, now)
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			counts, err := s.CompletionCounts(ctx, run.ID)
			require.NoError(t, err)
			require.EqualValues(t, 2, counts.Total)
			require.EqualValues(t, 1, counts.Pending)
			require.EqualValues(t, 1, counts.Sent)
			ids, err := s.ProviderOutcomeRunIDs(ctx, "provider")
			require.NoError(t, err)
			require.Equal(t, []string{run.ID}, ids)
			rows, err := s.PendingRecipients(ctx, run.ID, "", 10, now)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, b.ID, rows[0].ID)
			stats, err := s.LatestStats(ctx, c.ID)
			require.NoError(t, err)
			require.EqualValues(t, 1, stats.TotalSent)
			page, err := s.ListRecipients(ctx, c.ID, 1, 0)
			require.NoError(t, err)
			require.EqualValues(t, 2, page.Total)
			require.Len(t, page.Recipients, 1)
			return nil
		})
	},
	"provider recipient status compare and set and business counts": func(t *testing.T, h Harness) {
		run := insertRun(t, h, insertCampaign(t, h), "sending")
		r := recipient(t, h, run.ID)
		now := at()
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			return s.StoreRecipientResult(ctx, campaign.RecipientResult{RecipientID: r.ID, Status: "sent", ProviderMessageID: "provider"}, run.ID, now)
		})
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			rows, err := s.LockProviderRecipients(ctx, run.ID, "provider")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			n, err := s.UpdateProviderRecipient(ctx, rows[0], "delivered", now, nil)
			require.NoError(t, err)
			require.Equal(t, 1, n)
			n, err = s.UpdateProviderRecipient(ctx, rows[0], "bounced", now, nil)
			require.NoError(t, err)
			require.Zero(t, n)
			counts, err := s.CompletionCounts(ctx, run.ID)
			if err != nil {
				return err
			}
			return s.RefreshBusinessCounts(ctx, run.ID, counts)
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			v, err := s.FindRun(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, 1, v.SentCount)
			return nil
		})
	},
}

func init() {
	campaignLocks := map[string]func(context.Context, campaign.Store, string) error{
		"campaign": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.LockCampaign(ctx, id)
			return err
		},
		"content document owner": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.CampaignDocumentID(ctx, id, true)
			return err
		},
		"translation source": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.CampaignSourceLocale(ctx, id, true)
			return err
		},
		"AI share": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.CampaignAIDocumentRoot(ctx, id, emailcontent.ShareLock)
			return err
		},
		"AI update": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.CampaignAIDocumentRoot(ctx, id, emailcontent.UpdateLock)
			return err
		},
	}
	for name, lock := range campaignLocks {
		storeContract[name+" lock waits for first commit"] = func(t *testing.T, h Harness) {
			c := insertCampaign(t, h)
			name := "committed"
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error { return lock(ctx, s, c.ID) }, func(ctx context.Context, s campaign.Store) error {
				return s.UpdateCampaign(ctx, c, campaign.CampaignChange{Name: &name, UpdatedAt: at()})
			}, func(ctx context.Context, s campaign.Store) error {
				v, err := s.LockCampaign(ctx, c.ID)
				require.NoError(t, err)
				require.Equal(t, name, v.Name)
				return nil
			}))
		}
	}
	runLocks := map[string]func(context.Context, campaign.Store, string) error{
		"run": func(ctx context.Context, s campaign.Store, id string) error { _, err := s.LockRun(ctx, id); return err },
		"campaign then run": func(ctx context.Context, s campaign.Store, id string) error {
			_, _, err := s.LockDeliveryRun(ctx, id, false)
			return err
		},
	}
	for name, lock := range runLocks {
		storeContract[name+" lock serializes delivery writers"] = func(t *testing.T, h Harness) {
			run := insertRun(t, h, insertCampaign(t, h), "scheduled")
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error { return lock(ctx, s, run.ID) }, func(ctx context.Context, s campaign.Store) error { return s.SetTargetCount(ctx, run.ID, 7) }, func(ctx context.Context, s campaign.Store) error {
				v, err := s.LockRun(ctx, run.ID)
				require.NoError(t, err)
				require.Equal(t, 7, v.TargetCount)
				return nil
			}))
		}
	}
	recipientLocks := map[string]func(context.Context, campaign.Store, string) error{
		"claim": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.LockClaimRecipient(ctx, id)
			return err
		},
		"release": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.LockReleaseRecipient(ctx, id)
			return err
		},
		"result": func(ctx context.Context, s campaign.Store, id string) error {
			_, err := s.LockResultRecipient(ctx, id)
			return err
		},
	}
	for name, lock := range recipientLocks {
		storeContract[name+" recipient lock serializes workers"] = func(t *testing.T, h Harness) {
			run := insertRun(t, h, insertCampaign(t, h), "sending")
			r := recipient(t, h, run.ID)
			token := uuid.NewString()
			now := at()
			require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error { return lock(ctx, s, r.ID) }, func(ctx context.Context, s campaign.Store) error {
				_, err := s.ClaimRecipient(ctx, r, token, now, now.Add(time.Minute))
				return err
			}, func(ctx context.Context, s campaign.Store) error {
				v, err := s.LockClaimRecipient(ctx, r.ID)
				require.NoError(t, err)
				require.Equal(t, &token, v.DeliveryClaimID)
				return nil
			}))
		}
	}
}

func SeedCampaign(t *testing.T, h Harness) campaign.Campaign { return insertCampaign(t, h) }
func SeedRun(t *testing.T, h Harness, c campaign.Campaign, status string) campaign.DeliveryRun {
	return insertRun(t, h, c, status)
}

func init() {
	storeContract["source metadata and render rows retain locale ordering"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		subject := "Target"
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			_, err := s.InsertLocale(ctx, c.ID, "ko", &subject, at())
			return err
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			rows, err := s.CampaignLocaleMetadata(ctx, c.ID)
			require.NoError(t, err)
			require.Equal(t, []campaign.CampaignLocaleMetadata{{Locale: "en", Subject: &c.Subject}, {Locale: "ko", Subject: &subject}}, rows)
			render, err := s.CampaignRenderLocales(ctx, c.ID)
			require.NoError(t, err)
			require.Len(t, render, 2)
			require.True(t, render[0].IsSourceLocale)
			require.Equal(t, "en", render[0].Locale)
			require.False(t, render[1].IsSourceLocale)
			return nil
		})
	}
	storeContract["run start compare and set cannot replace a started run"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		run := insertRun(t, h, c, "scheduled")
		now := at()
		run.Status = "sending"
		run.TargetCount = 2
		transact(t, h, func(ctx context.Context, s campaign.Store) error { return s.StartRun(ctx, run, now) })
		run.TargetCount = 99
		transact(t, h, func(ctx context.Context, s campaign.Store) error { return s.StartRun(ctx, run, now.Add(time.Hour)) })
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			v, err := s.FindRun(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, "sending", v.Status)
			require.Equal(t, 2, v.TargetCount)
			require.Equal(t, &now, v.StartedAt)
			return nil
		})
	}
	storeContract["campaign dispatch compare and set rejects a draft"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			changed, err := s.DispatchCampaign(ctx, c.ID, "ALL_MATCHING_USERS")
			require.NoError(t, err)
			require.False(t, changed)
			c.Status = campaign.CampaignStatusScheduled
			return s.SaveCampaign(ctx, &c)
		})
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			changed, err := s.DispatchCampaign(ctx, c.ID, "ALL_MATCHING_USERS")
			require.NoError(t, err)
			require.True(t, changed)
			return nil
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			v, err := s.FindCampaign(ctx, c.ID)
			require.NoError(t, err)
			require.Equal(t, campaign.CampaignStatusSending, v.Status)
			require.Equal(t, "ALL_MATCHING_USERS", v.RecipientScope)
			require.Nil(t, v.ScheduledAt)
			return nil
		})
	}
	storeContract["durable queue command commits and rolls back with campaign"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		job := &managev1.SendEmailEvent{Recipient: "contract@example.test", TemplateType: "campaign:" + c.ID}
		messageID := uuid.NewString()
		job.MessageId = &messageID
		require.ErrorIs(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			if err := s.Enqueue(ctx, eventpkg.QueueEmailSend, messageID, job); err != nil {
				return err
			}
			return errRollback
		}), errRollback)
		require.Empty(t, h.QueueMessages(t))
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			return s.Enqueue(ctx, eventpkg.QueueEmailSend, messageID, job)
		})
		rows := h.QueueMessages(t)
		require.Len(t, rows, 1)
		require.True(t, proto.Equal(job, rows[0]))
	}
	storeContract["provider recipient row locks serialize outcomes"] = func(t *testing.T, h Harness) {
		run := insertRun(t, h, insertCampaign(t, h), "sending")
		r := recipient(t, h, run.ID)
		now := at()
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			return s.StoreRecipientResult(ctx, campaign.RecipientResult{RecipientID: r.ID, Status: "sent", ProviderMessageID: "provider"}, run.ID, now)
		})
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			_, err := s.LockProviderRecipients(ctx, run.ID, "provider")
			return err
		}, func(ctx context.Context, s campaign.Store) error {
			r.Status = "sent"
			_, err := s.UpdateProviderRecipient(ctx, r, "delivered", now, nil)
			return err
		}, func(ctx context.Context, s campaign.Store) error {
			rows, err := s.LockProviderRecipients(ctx, run.ID, "provider")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "delivered", rows[0].Status)
			return nil
		}))
	}
	storeContract["cancel locks the campaign before active run updates"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		run := insertRun(t, h, c, "scheduled")
		now := at()
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error { return s.CancelActiveRuns(ctx, c.ID, now) }, func(context.Context, campaign.Store) error { return nil }, func(ctx context.Context, s campaign.Store) error {
			_, err := s.LockCampaign(ctx, c.ID)
			if err != nil {
				return err
			}
			v, err := s.LockRun(ctx, run.ID)
			require.NoError(t, err)
			require.Equal(t, "cancelled", v.Status)
			return nil
		}))
	}
}

func init() {
	storeContract["source subject insert detects a duplicate and update keeps an exact locale"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		now := at()
		require.Error(t, mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			return s.InsertSourceSubject(ctx, c.ID, "en", "duplicate", now)
		}))
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			if err := s.InsertSourceSubject(ctx, c.ID, "ko", "Target", now); err != nil {
				return err
			}
			return s.UpdateSourceSubject(ctx, c.ID, "en", "New source", now)
		})
		read(t, h, func(ctx context.Context, s campaign.Store) error {
			source, exists, err := s.CampaignLocaleSubject(ctx, c.ID, "en", false)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "New source", *source)
			target, exists, err := s.CampaignLocaleSubject(ctx, c.ID, "ko", false)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "Target", *target)
			return nil
		})
	}
}

func init() {
	storeContract["source locale subject row lock serializes metadata writes"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			_, _, err := s.CampaignLocaleSubject(ctx, c.ID, "en", true)
			return err
		}, func(ctx context.Context, s campaign.Store) error {
			return s.UpdateSourceSubject(ctx, c.ID, "en", "After lock", at())
		}, func(ctx context.Context, s campaign.Store) error {
			subject, exists, err := s.CampaignLocaleSubject(ctx, c.ID, "en", true)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "After lock", *subject)
			return nil
		}))
	}
	storeContract["content revision compare and set rejects a stale writer"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		id := uuid.MustParse(*c.ContentDocumentID)
		fence := func(context.Context, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		}
		changed := func(context.Context) (contentblock.MetadataEffect, error) {
			return contentblock.MetadataEffect{Changed: true}, nil
		}
		var first contentblock.AdvanceResult
		transact(t, h, func(ctx context.Context, s campaign.Store) error {
			snapshot, err := s.LockedContentSnapshot(ctx, id, "en")
			if err != nil {
				return err
			}
			first, err = s.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: id, ExpectedRevision: snapshot.Document.Revision}, fence, changed)
			return err
		})
		require.True(t, first.Changed)
		err := mutationtest.Transact(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			_, err := s.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: id, ExpectedRevision: uuid.New()}, fence, changed)
			return err
		})
		var stale *contentblock.StaleRevisionError
		require.ErrorAs(t, err, &stale)
		require.Equal(t, first.DocumentRevision, stale.CurrentRevision)
	}
	storeContract["content snapshots hold a share lock until the unit ends"] = func(t *testing.T, h Harness) {
		c := insertCampaign(t, h)
		id := uuid.MustParse(*c.ContentDocumentID)
		var revision uuid.UUID
		require.NoError(t, h.Units.ReadLocked(t.Context(), func(s campaign.Store) error {
			snapshot, err := s.ContentSnapshot(t.Context(), id, "en")
			revision = snapshot.Document.Revision
			return err
		}))
		fence := func(context.Context, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		}
		require.NoError(t, mutationtest.RequireSecondWaits(t, h.Harness, func(ctx context.Context, s campaign.Store) error {
			_, err := s.ContentSnapshot(ctx, id, "en")
			return err
		}, func(context.Context, campaign.Store) error { return nil }, func(ctx context.Context, s campaign.Store) error {
			result, err := s.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: id, ExpectedRevision: revision}, fence, func(context.Context) (contentblock.MetadataEffect, error) {
				return contentblock.MetadataEffect{Changed: true}, nil
			})
			require.NoError(t, err)
			require.True(t, result.Changed)
			return nil
		}))
	}
}
