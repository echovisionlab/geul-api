// Package campaigntest supplies the in-memory Campaign Store and its contract.
package campaigntest

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/emailcontent/emailcontenttest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type state struct {
	campaigns  map[string]campaign.Campaign
	runs       map[string]campaign.DeliveryRun
	recipients map[string]campaign.DeliveryRecipient
	targets    map[string]campaign.CampaignDeliveryTarget
	routes     []string
	queued     []proto.Message
}

func (v state) clone() state {
	clone := v
	clone.campaigns = mutationtest.ClonePersisted(v.campaigns)
	clone.runs = mutationtest.ClonePersisted(v.runs)
	clone.recipients = mutationtest.ClonePersisted(v.recipients)
	clone.targets = mutationtest.ClonePersisted(v.targets)
	clone.routes = mutationtest.ClonePersisted(v.routes)
	clone.queued = mutationtest.ClonePersisted(v.queued)
	return clone
}

type Store struct {
	*emailcontenttest.Store
	mu sync.Mutex
	state
	failures          map[string]error
	Members           map[string]bool
	Sessions          map[string]*auth.UserInfo
	CriteriaSet       map[string]campaign.CampaignCriteriaTarget
	Layouts           map[string]campaign.CampaignLayoutReference
	LayoutLocales     map[string][]campaign.CampaignLayoutLocaleSnapshot
	Candidates        []campaign.DeliveryRecipient
	Renderable        map[string]bool
	ContributorError  error
	ReadinessError    error
	RenderData        map[string]string
	Wrap              func(string, string, string, map[string]string) (string, error)
	LegalPreviewError error
	Calls             []string
}

var _ campaign.Store = (*Store)(nil)

func New() (*Store, *mutationtest.UnitOfWork[campaign.Store]) {
	s := &Store{Store: emailcontenttest.New(), state: state{campaigns: map[string]campaign.Campaign{}, runs: map[string]campaign.DeliveryRun{}, recipients: map[string]campaign.DeliveryRecipient{}, targets: map[string]campaign.CampaignDeliveryTarget{}}, Members: map[string]bool{}, Sessions: map[string]*auth.UserInfo{}, CriteriaSet: map[string]campaign.CampaignCriteriaTarget{}, Layouts: map[string]campaign.CampaignLayoutReference{}, LayoutLocales: map[string][]campaign.CampaignLayoutLocaleSnapshot{}, Renderable: map[string]bool{}, failures: map[string]error{}}
	s.Touched = func(id string, now time.Time) {
		s.mu.Lock()
		defer s.mu.Unlock()
		row, ok := s.campaigns[id]
		if ok {
			row.UpdatedAt = now
			s.campaigns[id] = row
		}
	}
	return s, mutationtest.New[campaign.Store](s)
}
func (s *Store) Savepoint() mutationtest.Checkpoint {
	content := s.Store.Savepoint()
	s.mu.Lock()
	saved := s.state.clone()
	s.mu.Unlock()
	return mutationtest.Checkpoint{
		State: map[string]any{
			"campaigns":  saved.campaigns,
			"runs":       saved.runs,
			"recipients": saved.recipients,
			"targets":    saved.targets,
			"routes":     saved.routes,
			"queued":     saved.queued,
			"content":    content.State,
		},
		Restore: func() { content.Restore(); s.mu.Lock(); s.state = saved; s.mu.Unlock() },
	}
}
func (s *Store) Fail(method string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[method] = err
}
func (s *Store) fail(method string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Calls = append(s.Calls, method)
	return s.failures[method]
}
func (s *Store) PutCampaign(v campaign.Campaign) campaign.Campaign {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.SourceLocale == "" {
		v.SourceLocale = "en"
	}
	if v.Status == "" {
		v.Status = campaign.CampaignStatusDraft
	}
	if v.TargetMode == "" {
		v.TargetMode = campaign.CampaignTargetModeAll
	}
	if v.RecipientScope == "" {
		v.RecipientScope = "SUBSCRIBED_USERS"
	}
	if v.Subject == "" {
		v.Subject = "Subject"
	}
	if v.Name == "" {
		v.Name = v.Subject
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
		v.UpdatedAt = v.CreatedAt
	}
	if v.ContentDocumentID == nil {
		document := s.PutDocument(emailcontenttest.Document{})
		id := document.ID.String()
		v.ContentDocumentID = &id
	}
	subject := v.Subject
	s.PutLocale(v.ID, emailcontenttest.Locale{LocaleMetadata: emailcontent.LocaleMetadata{Locale: v.SourceLocale, Subject: &subject, UpdatedAt: v.CreatedAt}})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[v.ID] = v
	return v
}
func (s *Store) Campaign(id string) (campaign.Campaign, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.campaigns[id]
	return v, ok
}
func (s *Store) PutRun(v campaign.DeliveryRun, target campaign.CampaignDeliveryTarget) campaign.DeliveryRun {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
		v.UpdatedAt = v.CreatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[v.ID] = v
	s.targets[v.ID] = target
	return v
}
func (s *Store) Run(id string) (campaign.DeliveryRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.runs[id]
	return v, ok
}
func (s *Store) PutRecipient(v campaign.DeliveryRecipient) campaign.DeliveryRecipient {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.Status == "" {
		v.Status = "pending"
	}
	if v.NormalizedRecipientEmail == "" {
		v.NormalizedRecipientEmail = strings.ToLower(strings.TrimSpace(v.RecipientEmail))
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recipients[v.ID] = v
	return v
}
func (s *Store) Recipient(id string) (campaign.DeliveryRecipient, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.recipients[id]
	return v, ok
}
func (s *Store) Queued() []proto.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queued)
}
func (s *Store) FindCampaign(_ context.Context, id string) (campaign.Campaign, error) {
	if err := s.fail("FindCampaign"); err != nil {
		return campaign.Campaign{}, err
	}
	v, ok := s.Campaign(id)
	if !ok {
		return v, campaign.ErrCampaignNotFound
	}
	return v, nil
}
func (s *Store) LockCampaign(ctx context.Context, id string) (campaign.Campaign, error) {
	if err := s.fail("LockCampaign"); err != nil {
		return campaign.Campaign{}, err
	}
	return s.FindCampaign(ctx, id)
}
func (s *Store) InsertCampaign(_ context.Context, v *campaign.Campaign) error {
	if err := s.fail("InsertCampaign"); err != nil {
		return err
	}
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.RecipientScope == "" {
		v.RecipientScope = "SUBSCRIBED_USERS"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[v.ID] = *v
	return nil
}
func (s *Store) SaveCampaign(_ context.Context, v *campaign.Campaign) error {
	if err := s.fail("SaveCampaign"); err != nil {
		return err
	}
	v.UpdatedAt = time.Now().UTC().Truncate(time.Microsecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[v.ID] = *v
	return nil
}
func (s *Store) UpdateCampaign(_ context.Context, v campaign.Campaign, change campaign.CampaignChange) error {
	if err := s.fail("UpdateCampaign"); err != nil {
		return err
	}
	if change.Name != nil {
		v.Name = *change.Name
	}
	if c := change.Configuration; c != nil {
		v.TargetMode, v.CriteriaID, v.LayoutID, v.RecipientScope = c.TargetMode, c.CriteriaID, c.LayoutID, c.RecipientScope
	}
	v.UpdatedAt = change.UpdatedAt
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[v.ID] = v
	return nil
}
func (s *Store) UpdateCampaignStatus(_ context.Context, v campaign.Campaign, status campaign.CampaignStatus) error {
	if err := s.fail("UpdateCampaignStatus"); err != nil {
		return err
	}
	v.Status = status
	s.mu.Lock()
	defer s.mu.Unlock()
	s.campaigns[v.ID] = v
	return nil
}
func (s *Store) DeleteCampaign(_ context.Context, v campaign.Campaign) error {
	if err := s.fail("DeleteCampaign"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.CampaignID != nil && *run.CampaignID == v.ID {
			return campaign.ErrDurableReference
		}
	}
	delete(s.campaigns, v.ID)
	return nil
}
func (s *Store) ListCampaigns(_ context.Context, q campaign.CampaignList) ([]campaign.Campaign, int64, error) {
	if err := s.fail("ListCampaigns"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := slices.Collect(maps.Values(s.campaigns))
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	total := int64(len(rows))
	offset := min(max(q.Offset, 0), len(rows))
	end := len(rows)
	if q.Limit > 0 {
		end = min(end, offset+q.Limit)
	}
	return rows[offset:end], total, nil
}
func (s *Store) CampaignLayoutID(ctx context.Context, id string) (*string, error) {
	v, err := s.FindCampaign(ctx, id)
	return v.LayoutID, err
}
func (s *Store) CampaignDocumentID(ctx context.Context, id string, lock bool) (*string, error) {
	var v campaign.Campaign
	var err error
	if lock {
		v, err = s.LockCampaign(ctx, id)
	} else {
		v, err = s.FindCampaign(ctx, id)
	}
	return v.ContentDocumentID, err
}
func (s *Store) CampaignSourceLocale(ctx context.Context, id string, lock bool) (string, error) {
	var v campaign.Campaign
	var err error
	if lock {
		v, err = s.LockCampaign(ctx, id)
	} else {
		v, err = s.FindCampaign(ctx, id)
	}
	return v.SourceLocale, err
}
func (s *Store) CampaignStatus(ctx context.Context, id string) (campaign.CampaignStatus, error) {
	v, err := s.FindCampaign(ctx, id)
	return v.Status, err
}
func (s *Store) CampaignAIDocumentRoot(ctx context.Context, id string, _ emailcontent.RowLock) (campaign.Campaign, error) {
	return s.LockCampaign(ctx, id)
}
func (s *Store) CampaignSourceLocales(_ context.Context, ids []string) (map[string]string, error) {
	if err := s.fail("CampaignSourceLocales"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]string{}
	for _, id := range ids {
		if v, ok := s.campaigns[id]; ok {
			result[id] = v.SourceLocale
		}
	}
	return result, nil
}
func (s *Store) ActiveCampaignRunCount(_ context.Context, id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, v := range s.runs {
		if v.CampaignID != nil && *v.CampaignID == id && (v.Status == "scheduled" || v.Status == "sending") {
			n++
		}
	}
	return n, nil
}
func (s *Store) RenderableCampaignContentCount(_ context.Context, id string) (int64, error) {
	if s.Renderable[id] {
		return 1, nil
	}
	return 0, nil
}
func (s *Store) SetSourceSubject(_ context.Context, id, subject string, now time.Time) error {
	if err := s.fail("SetSourceSubject"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.campaigns[id]
	v.Subject, v.UpdatedAt = subject, now
	s.campaigns[id] = v
	return nil
}
func (s *Store) EnsureCampaignRoute(_ context.Context, id string) error {
	if err := s.fail("EnsureCampaignRoute"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = append(s.routes, id)
	return nil
}
func (s *Store) LockLayouts(_ context.Context, ids ...string) (map[string]campaign.CampaignLayoutReference, error) {
	if err := s.fail("LockLayouts"); err != nil {
		return nil, err
	}
	result := map[string]campaign.CampaignLayoutReference{}
	for _, id := range ids {
		if v, ok := s.Layouts[id]; ok {
			result[id] = v
		}
	}
	return result, nil
}
func (s *Store) LayoutSnapshot(_ context.Context, id string) ([]campaign.CampaignLayoutLocaleSnapshot, error) {
	return slices.Clone(s.LayoutLocales[id]), s.fail("LayoutSnapshot")
}
func (s *Store) LockCriteriaTarget(_ context.Context, id string) (campaign.CampaignCriteriaTarget, error) {
	if err := s.fail("LockCriteriaTarget"); err != nil {
		return campaign.CampaignCriteriaTarget{}, err
	}
	v, ok := s.CriteriaSet[id]
	if !ok {
		return v, campaign.ErrCriteriaNotFound
	}
	return v, nil
}
func (s *Store) ResolveSessionPrincipal(_ context.Context, id string) (*auth.UserInfo, error) {
	v := s.Sessions[id]
	if v == nil {
		return nil, auth.ErrSessionPrincipalInvalid
	}
	return v, nil
}
func (s *Store) LockMembers(_ context.Context, ids []string) ([]string, error) {
	if err := s.fail("LockMembers"); err != nil {
		return nil, err
	}
	var found []string
	for _, id := range ids {
		if s.Members[id] {
			found = append(found, id)
		}
	}
	return found, nil
}
func (s *Store) RequireCurrentContributors(context.Context, string, []string) error {
	if err := s.fail("RequireCurrentContributors"); err != nil {
		return err
	}
	return s.ContributorError
}
func (s *Store) EmailRenderData(_ context.Context, _, _, _ string, input map[string]string) map[string]string {
	result := maps.Clone(input)
	maps.Copy(result, s.RenderData)
	return result
}
func (s *Store) WrapWithLayout(_ context.Context, id, locale, html string, data map[string]string) (string, error) {
	if s.Wrap != nil {
		return s.Wrap(id, locale, html, data)
	}
	return html, s.fail("WrapWithLayout")
}
func (s *Store) CheckTestSendReadiness(context.Context, string) error {
	if err := s.fail("CheckTestSendReadiness"); err != nil {
		return err
	}
	return s.ReadinessError
}
func (s *Store) Enqueue(_ context.Context, _, _ string, message proto.Message) error {
	if err := s.fail("Enqueue"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queued = append(s.queued, proto.Clone(message))
	return nil
}
func (s *Store) CountRecipients(context.Context, campaign.CampaignDeliveryTarget) (int64, error) {
	if err := s.fail("CountRecipients"); err != nil {
		return 0, err
	}
	return int64(len(s.Candidates)), nil
}
func (s *Store) PrepareLegalNoticePreview(context.Context, campaign.DeliveryRun, time.Time) error {
	return s.LegalPreviewError
}
func (s *Store) InsertSealedRun(_ context.Context, v *campaign.DeliveryRun, target *campaign.CampaignDeliveryTarget) error {
	if err := s.fail("InsertSealedRun"); err != nil {
		return err
	}
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	v.DefinitionSealed = true
	var t campaign.CampaignDeliveryTarget
	if target != nil {
		t = *target
	}
	*v = s.PutRun(*v, t)
	return nil
}
func (s *Store) FindRun(_ context.Context, id string) (campaign.DeliveryRun, error) {
	if err := s.fail("FindRun"); err != nil {
		return campaign.DeliveryRun{}, err
	}
	v, ok := s.Run(id)
	if !ok {
		return v, campaign.ErrNotFound
	}
	return v, nil
}
func (s *Store) LockRun(ctx context.Context, id string) (campaign.DeliveryRun, error) {
	if err := s.fail("LockRun"); err != nil {
		return campaign.DeliveryRun{}, err
	}
	return s.FindRun(ctx, id)
}
func (s *Store) LockDeliveryRun(ctx context.Context, id string, _ bool) (campaign.LockedDeliveryRun, bool, error) {
	if err := s.fail("LockDeliveryRun"); err != nil {
		return campaign.LockedDeliveryRun{}, false, err
	}
	run, err := s.FindRun(ctx, id)
	if err != nil {
		return campaign.LockedDeliveryRun{}, false, err
	}
	v := campaign.LockedDeliveryRun{Run: run}
	if run.RunKind == campaign.EmailDeliveryRunKindCampaign {
		if run.CampaignID == nil {
			return v, false, errors.New("campaign delivery run missing campaign id")
		}
		c, err := s.LockCampaign(ctx, *run.CampaignID)
		if err != nil {
			return v, false, err
		}
		v.Campaign = &c
	}
	return v, false, nil
}
func (s *Store) SaveRun(_ context.Context, v *campaign.DeliveryRun) error {
	if err := s.fail("SaveRun"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[v.ID] = *v
	return nil
}
func (s *Store) StartRun(_ context.Context, v campaign.DeliveryRun, now time.Time) error {
	if err := s.fail("StartRun"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.runs[v.ID]
	if ok && current.Status == "scheduled" {
		current.Status, current.StartedAt, current.TargetCount = v.Status, &now, v.TargetCount
		s.runs[v.ID] = current
	}
	return nil
}
func (s *Store) FailRun(_ context.Context, id string, now time.Time) error {
	if err := s.fail("FailRun"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.runs[id]
	message := "campaign has no recipients"
	v.Status, v.LastError, v.CompletedAt = "failed", &message, &now
	s.runs[id] = v
	return nil
}
func (s *Store) DispatchRun(_ context.Context, v campaign.DeliveryRun, now time.Time) error {
	if err := s.fail("DispatchRun"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.runs[v.ID]
	current.Status, current.TargetCount = v.Status, v.TargetCount
	if v.CompletedAt != nil {
		current.CompletedAt = &now
	} else {
		current.StartedAt = &now
	}
	s.runs[v.ID] = current
	return nil
}
func (s *Store) DispatchCampaign(_ context.Context, id, scope string) (bool, error) {
	if err := s.fail("DispatchCampaign"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.campaigns[id]
	if !ok || (v.Status != campaign.CampaignStatusScheduled && v.Status != campaign.CampaignStatusSending) {
		return false, nil
	}
	v.Status = campaign.CampaignStatusSending
	v.ScheduledAt, v.SentAt = nil, nil
	v.SentCount = 0
	v.RecipientScope = scope
	s.campaigns[id] = v
	return true, nil
}
func (s *Store) DueRuns(_ context.Context, now time.Time, limit int) ([]campaign.DeliveryRun, error) {
	if err := s.fail("DueRuns"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []campaign.DeliveryRun
	for _, v := range s.runs {
		if v.Status == "scheduled" && !v.ScheduledAt.After(now) {
			rows = append(rows, v)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ScheduledAt.Equal(rows[j].ScheduledAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].ScheduledAt.Before(rows[j].ScheduledAt)
	})
	return rows[:min(len(rows), limit)], nil
}
func (s *Store) ActiveRuns(_ context.Context, limit int) ([]campaign.DeliveryRun, error) {
	if err := s.fail("ActiveRuns"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []campaign.DeliveryRun
	for _, v := range s.runs {
		if v.Status == "sending" && v.CompletedAt == nil {
			rows = append(rows, v)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].UpdatedAt.Before(rows[j].UpdatedAt)
	})
	return rows[:min(len(rows), limit)], nil
}
func (s *Store) LoadDeliveryTarget(_ context.Context, v campaign.DeliveryRun) (campaign.CampaignDeliveryTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.targets[v.ID]
	t.QueryVersion, t.Mode, t.RecipientScope = v.TargetQueryVersion, v.TargetMode, v.TargetRecipientScope
	t.CreatedAfter, t.CreatedBefore = v.TargetCreatedAfter, v.TargetCreatedBefore
	return t, nil
}
func (s *Store) CancelActiveRuns(_ context.Context, id string, now time.Time) error {
	if err := s.fail("CancelActiveRuns"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.campaigns[id]; !ok {
		return campaign.ErrNotFound
	}
	for _, v := range s.runs {
		if v.CampaignID != nil && *v.CampaignID == id && v.Status == "sending" {
			return campaign.ErrDeliveryStarted
		}
	}
	for key, v := range s.runs {
		if v.CampaignID != nil && *v.CampaignID == id && v.Status == "scheduled" {
			v.Status = "cancelled"
			v.CompletedAt = &now
			s.runs[key] = v
		}
	}
	return nil
}
func (s *Store) HasHistory(_ context.Context, id string) (bool, error) {
	if err := s.fail("HasHistory"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.runs {
		if v.CampaignID != nil && *v.CampaignID == id {
			return true, nil
		}
	}
	return false, nil
}
func (s *Store) latest(id string) (campaign.DeliveryRun, bool) {
	var found campaign.DeliveryRun
	ok := false
	for _, v := range s.runs {
		if v.CampaignID != nil && *v.CampaignID == id && (!ok || v.CreatedAt.After(found.CreatedAt) || (v.CreatedAt.Equal(found.CreatedAt) && v.ID > found.ID)) {
			found = v
			ok = true
		}
	}
	return found, ok
}
func (s *Store) LatestStats(_ context.Context, id string) (*campaign.CampaignDeliveryStats, error) {
	if err := s.fail("LatestStats"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.latest(id)
	if !ok {
		return nil, nil
	}
	v := campaign.CampaignDeliveryStats{}
	for _, r := range s.recipients {
		if r.RunID != run.ID {
			continue
		}
		switch r.Status {
		case "sent", "delivered":
			v.TotalSent++
		case "skipped":
			v.Skipped++
		case "permanent_failed", "bounced", "complained":
			v.Failed++
		case "blocked":
			v.Blocked++
		case "suppressed":
			v.Suppressed++
		}
	}
	return &v, nil
}
func (s *Store) ListRecipients(_ context.Context, id string, limit, offset int) (campaign.CampaignDeliveryRecipientPage, error) {
	if err := s.fail("ListRecipients"); err != nil {
		return campaign.CampaignDeliveryRecipientPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.latest(id)
	if !ok {
		return campaign.CampaignDeliveryRecipientPage{}, nil
	}
	var rows []campaign.DeliveryRecipient
	for _, r := range s.recipients {
		if r.RunID == run.ID {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID > rows[j].ID
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	page := campaign.CampaignDeliveryRecipientPage{Total: int64(len(rows))}
	offset = min(max(offset, 0), len(rows))
	end := len(rows)
	if limit > 0 {
		end = min(end, offset+limit)
	}
	page.Recipients = make([]campaign.CampaignDeliveryRecipient, end-offset)
	for i, r := range rows[offset:end] {
		member := ""
		if r.MemberID != nil {
			member = strings.TrimSpace(*r.MemberID)
		}
		page.Recipients[i] = campaign.CampaignDeliveryRecipient{Email: r.RecipientEmail, Status: r.Status, MemberID: member, ErrorType: r.ErrorType, TerminalAt: r.TerminalAt}
	}
	return page, nil
}
func (s *Store) MaterializeRecipients(_ context.Context, run campaign.DeliveryRun, _ campaign.CampaignDeliveryTarget) error {
	if err := s.fail("MaterializeRecipients"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, v := range s.recipients {
		if v.RunID == run.ID {
			seen[v.NormalizedRecipientEmail] = true
		}
	}
	for _, v := range s.Candidates {
		key := strings.ToLower(strings.TrimSpace(v.RecipientEmail))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		v.ID, v.RunID, v.Status, v.NormalizedRecipientEmail = uuid.NewString(), run.ID, "pending", key
		s.recipients[v.ID] = v
	}
	return nil
}
func (s *Store) RecipientCount(_ context.Context, id string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, v := range s.recipients {
		if v.RunID == id {
			n++
		}
	}
	return n, nil
}
func (s *Store) SetTargetCount(_ context.Context, id string, n int) error {
	if err := s.fail("SetTargetCount"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.runs[id]
	v.TargetCount = n
	s.runs[id] = v
	return nil
}
func (s *Store) PendingRecipients(_ context.Context, id, after string, limit int, now time.Time) ([]campaign.DeliveryRecipient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []campaign.DeliveryRecipient
	for _, v := range s.recipients {
		if v.RunID == id && v.Status == "pending" && v.ID > after && (v.DeliveryClaimID == nil || (v.DeliveryClaimExpiresAt != nil && !v.DeliveryClaimExpiresAt.After(now))) {
			rows = append(rows, v)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows[:min(len(rows), limit)], nil
}
func (s *Store) HasPendingRecipients(_ context.Context, id string) (bool, error) {
	if err := s.fail("HasPendingRecipients"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.recipients {
		if v.RunID == id && v.Status == "pending" {
			return true, nil
		}
	}
	return false, nil
}
func (s *Store) FindRecipient(_ context.Context, id string) (campaign.DeliveryRecipient, error) {
	if err := s.fail("FindRecipient"); err != nil {
		return campaign.DeliveryRecipient{}, err
	}
	v, ok := s.Recipient(id)
	if !ok {
		return v, campaign.ErrNotFound
	}
	return v, nil
}
func (s *Store) LockClaimRecipient(ctx context.Context, id string) (campaign.DeliveryRecipient, error) {
	if err := s.fail("LockClaimRecipient"); err != nil {
		return campaign.DeliveryRecipient{}, err
	}
	v, err := s.FindRecipient(ctx, id)
	return campaign.DeliveryRecipient{ID: v.ID, RunID: v.RunID, Status: v.Status, DeliveryClaimID: v.DeliveryClaimID, DeliveryClaimExpiresAt: v.DeliveryClaimExpiresAt}, err
}
func (s *Store) LockResultRecipient(ctx context.Context, id string) (campaign.DeliveryRecipient, error) {
	if err := s.fail("LockResultRecipient"); err != nil {
		return campaign.DeliveryRecipient{}, err
	}
	v, err := s.FindRecipient(ctx, id)
	return campaign.DeliveryRecipient{ID: v.ID, RunID: v.RunID, Status: v.Status, DeliveryClaimID: v.DeliveryClaimID, DeliveryClaimExpiresAt: v.DeliveryClaimExpiresAt, ProviderMessageID: v.ProviderMessageID, ErrorType: v.ErrorType, TerminalAt: v.TerminalAt}, err
}
func (s *Store) ClaimRecipient(_ context.Context, identity campaign.DeliveryRecipient, claim string, now, expires time.Time) (bool, error) {
	if err := s.fail("ClaimRecipient"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.recipients[identity.ID]
	if !ok || v.RunID != identity.RunID || v.Status != "pending" || (v.DeliveryClaimID != nil && (v.DeliveryClaimExpiresAt == nil || v.DeliveryClaimExpiresAt.After(now))) {
		return false, nil
	}
	v.DeliveryClaimID, v.DeliveryClaimExpiresAt = &claim, &expires
	s.recipients[v.ID] = v
	return true, nil
}
func (s *Store) ReleaseRecipientClaim(_ context.Context, identity campaign.DeliveryRecipient, claim string) error {
	if err := s.fail("ReleaseRecipientClaim"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.recipients[identity.ID]
	if ok && v.RunID == identity.RunID && v.Status == "pending" && v.DeliveryClaimID != nil && *v.DeliveryClaimID == claim {
		v.DeliveryClaimID, v.DeliveryClaimExpiresAt = nil, nil
		s.recipients[v.ID] = v
	}
	return nil
}
func nullable(value string) *string {
	if value = strings.TrimSpace(value); value == "" {
		return nil
	}
	return &value
}
func (s *Store) StoreRecipientResult(_ context.Context, r campaign.RecipientResult, runID string, now time.Time) error {
	if err := s.fail("StoreRecipientResult"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.recipients[r.RecipientID]
	live := v.DeliveryClaimID != nil && v.DeliveryClaimExpiresAt != nil && v.DeliveryClaimExpiresAt.After(now)
	matches := (!r.RequireClaim && (v.DeliveryClaimID == nil || (v.DeliveryClaimExpiresAt != nil && !v.DeliveryClaimExpiresAt.After(now)))) || (r.RequireClaim && live && *v.DeliveryClaimID == r.ClaimID)
	if !ok || v.RunID != runID || v.Status != "pending" || !matches {
		return campaign.ErrNotFound
	}
	v.Status, v.TerminalAt, v.ProviderMessageID, v.ErrorType = r.Status, &now, nullable(r.ProviderMessageID), nullable(r.ErrorType)
	v.DeliveryClaimID, v.DeliveryClaimExpiresAt = nil, nil
	s.recipients[v.ID] = v
	return nil
}
func (s *Store) CompletionCounts(_ context.Context, id string) (campaign.DeliveryCompletionCounts, error) {
	if err := s.fail("CompletionCounts"); err != nil {
		return campaign.DeliveryCompletionCounts{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := campaign.DeliveryCompletionCounts{}
	for _, r := range s.recipients {
		if r.RunID != id {
			continue
		}
		v.Total++
		switch r.Status {
		case "pending":
			v.Pending++
		case "sent":
			v.Sent++
		case "delivered":
			v.Delivered++
		case "skipped":
			v.Skipped++
		case "permanent_failed":
			v.PermanentFail++
		case "blocked":
			v.Blocked++
		case "suppressed":
			v.Suppressed++
		case "bounced":
			v.Bounced++
		case "complained":
			v.Complained++
		}
	}
	return v, nil
}
func (s *Store) ProviderOutcomeRunIDs(_ context.Context, message string) ([]string, error) {
	if err := s.fail("ProviderOutcomeRunIDs"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, v := range s.recipients {
		if v.ProviderMessageID != nil && *v.ProviderMessageID == message {
			seen[v.RunID] = true
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}
func (s *Store) LockProviderRecipients(_ context.Context, id, message string) ([]campaign.DeliveryRecipient, error) {
	if err := s.fail("LockProviderRecipients"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []campaign.DeliveryRecipient
	for _, v := range s.recipients {
		if v.RunID == id && v.ProviderMessageID != nil && *v.ProviderMessageID == message {
			rows = append(rows, v)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}
func (s *Store) UpdateProviderRecipient(_ context.Context, identity campaign.DeliveryRecipient, status string, at time.Time, errorType *string) (int, error) {
	if err := s.fail("UpdateProviderRecipient"); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.recipients[identity.ID]
	if !ok || v.Status != identity.Status {
		return 0, nil
	}
	v.Status, v.TerminalAt, v.UpdatedAt, v.ErrorType = status, &at, at, errorType
	s.recipients[v.ID] = v
	return 1, nil
}
func (s *Store) RefreshBusinessCounts(_ context.Context, id string, c campaign.DeliveryCompletionCounts) error {
	if err := s.fail("RefreshBusinessCounts"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.runs[id]
	v.SentCount, v.SkippedCount, v.FailedCount, v.BlockedCount, v.SuppressedCount = int(c.Sent+c.Delivered), int(c.Skipped), int(c.PermanentFail+c.Bounced+c.Complained), int(c.Blocked), int(c.Suppressed)
	s.runs[id] = v
	return nil
}

func (s *Store) CampaignLocaleMetadata(ctx context.Context, id string) ([]campaign.CampaignLocaleMetadata, error) {
	if err := s.fail("CampaignLocaleMetadata"); err != nil {
		return nil, err
	}
	names, err := s.LocaleNames(ctx, id)
	if err != nil {
		return nil, err
	}
	rows := make([]campaign.CampaignLocaleMetadata, 0, len(names))
	for _, locale := range names {
		row, _, err := s.LocaleRow(ctx, id, locale, emailcontent.Unlocked)
		if err != nil {
			return nil, err
		}
		rows = append(rows, campaign.CampaignLocaleMetadata{Locale: locale, Subject: row.Subject})
	}
	return rows, nil
}
func (s *Store) CampaignRenderLocales(ctx context.Context, id string) ([]campaign.CampaignRenderLocale, error) {
	if err := s.fail("CampaignRenderLocales"); err != nil {
		return nil, err
	}
	rows, err := s.CampaignLocaleMetadata(ctx, id)
	if err != nil {
		return nil, err
	}
	c, exists := s.Campaign(id)
	if !exists {
		return nil, nil
	}
	result := make([]campaign.CampaignRenderLocale, 0, len(rows))
	for _, row := range rows {
		result = append(result, campaign.CampaignRenderLocale{Locale: row.Locale, Subject: row.Subject, IsSourceLocale: row.Locale == c.SourceLocale})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].IsSourceLocale && !result[j].IsSourceLocale })
	return result, nil
}

func (s *Store) CampaignLocaleSubject(ctx context.Context, id, locale string, _ bool) (*string, bool, error) {
	row, exists, err := s.LocaleRow(ctx, id, locale, emailcontent.Unlocked)
	return row.Subject, exists, err
}
func (s *Store) InsertSourceSubject(ctx context.Context, id, locale, subject string, now time.Time) error {
	_, exists, err := s.LocaleRow(ctx, id, locale, emailcontent.Unlocked)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("campaign source locale already exists")
	}
	_, err = s.InsertLocale(ctx, id, locale, &subject, now)
	return err
}
func (s *Store) UpdateSourceSubject(ctx context.Context, id, locale, subject string, now time.Time) error {
	_, err := s.UpdateLocale(ctx, id, locale, &subject, now)
	return err
}
func (s *Store) RecipientIdentity(ctx context.Context, id string) (campaign.DeliveryRecipient, error) {
	v, err := s.FindRecipient(ctx, id)
	return campaign.DeliveryRecipient{ID: v.ID, RunID: v.RunID}, err
}
func (s *Store) RecipientStatus(ctx context.Context, id string) (string, error) {
	v, err := s.FindRecipient(ctx, id)
	return v.Status, err
}
func (s *Store) LockReleaseRecipient(ctx context.Context, id string) (campaign.DeliveryRecipient, error) {
	v, err := s.LockClaimRecipient(ctx, id)
	return campaign.DeliveryRecipient{ID: v.ID, RunID: v.RunID, Status: v.Status, DeliveryClaimID: v.DeliveryClaimID}, err
}
