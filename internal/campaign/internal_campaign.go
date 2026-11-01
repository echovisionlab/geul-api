package campaign

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// InternalDeps are the collaborators of the Campaign collaboration and AI
// document use cases. Every field is required; NewInternalService rejects a
// missing one.
type InternalDeps struct {
	Units       Units
	SpiceDB     mutation.Authorizer
	Fence       identitystate.Fence
	Permissions CollaborationPermissionChecker
	Audit       domainaudit.Appender
	Clock       func() time.Time
}

func (d InternalDeps) validate() error {
	return requireDependencies("internal campaign", map[string]bool{"Units": d.Units == nil, "SpiceDB": d.SpiceDB == nil, "Fence": d.Fence == nil, "Permissions": d.Permissions == nil, "Audit": d.Audit == nil, "Clock": d.Clock == nil})
}

type InternalService struct {
	units       Units
	permissions CollaborationPermissionChecker
	mutations   *mutation.StoreRunner[Store]
	now         func() time.Time
}

func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	runner, err := mutation.NewStoreRunner(deps.Units, deps.SpiceDB, deps.Fence, deps.Audit)
	if err != nil {
		return nil, err
	}
	return &InternalService{units: deps.Units, permissions: deps.Permissions, mutations: runner, now: deps.Clock}, nil
}

// ApplyCampaignBlockBatchCommand applies a collaboration room's Block batch
// to a Campaign in the room's locale.
type ApplyCampaignBlockBatchCommand struct {
	CampaignID             string
	Locale                 string
	ExpectedTargetRevision *string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// campaignCollaboration is one collaboration write of a Campaign room after
// its document and source locale were read.
type campaignCollaboration struct {
	campaignID   string
	locale       string
	documentID   uuid.UUID
	sourceLocale string
	contributors []string
	now          time.Time
}

// readCampaignCollaboration reads the room's Campaign document and source
// locale.
func (s *InternalService) readCampaignCollaboration(ctx context.Context, campaignID, locale string, contributors []string) (campaignCollaboration, error) {
	write := campaignCollaboration{campaignID: campaignID, contributors: contributors, now: s.now().UTC().Truncate(dbtime.Precision)}
	if write.campaignID == "" {
		return write, domainerr.Required("campaign_id")
	}
	var err error
	if write.locale, err = emailcontent.NormalizeLocale(locale); err != nil {
		return write, err
	}

	err = s.units.Read(ctx, func(store Store) error {
		var err error
		write.documentID, err = loadCampaignEmailContentDocumentRoot(ctx, store, campaignID, false)
		if err != nil {
			return err
		}
		domain, err := loadCampaignSourceContext(ctx, store, campaignID, false)
		write.sourceLocale = domain.SourceLocale
		return err
	})
	return write, err
}

// requireTargetOrigin rejects a target room write without exactly one origin
// Member, the Member its change is credited to.
func (w campaignCollaboration) requireTargetOrigin() (string, error) {
	if len(w.contributors) != 1 {
		return "", domainerr.InvalidField("contributor_member_ids", "target collaboration mutation requires exactly one origin Member")
	}
	return w.contributors[0], nil
}

// saveCampaignCollaboration runs one collaboration write after the room's
// contributors are locked and proven current.
func (s *InternalService) saveCampaignCollaboration(
	ctx context.Context,
	write campaignCollaboration,
	change func(ctx context.Context, tx Store) (emailcontent.CollaborationChange, error),
) (emailcontent.CollaborationChange, error) {
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[emailcontent.CollaborationChange, Store]{
		Access: emailcontent.CollaborationAccess,
		Change: func(ctx context.Context, tx Store) (emailcontent.CollaborationChange, error) {
			if err := requireCampaignContributors(ctx, tx, write.contributors); err != nil {
				return emailcontent.CollaborationChange{}, err
			}
			if err := tx.RequireCurrentContributors(
				ctx, write.campaignID, write.contributors,
			); err != nil {
				return emailcontent.CollaborationChange{}, err
			}
			return change(ctx, tx)
		},
		Audit: func(change emailcontent.CollaborationChange) mutation.Audit { return change.Record },
	})
}

// ApplyCampaignBlockBatch applies the batch. A source edit refreshes the
// source locale's materialized content; a target edit is credited to its
// origin Member.
func (s *InternalService) ApplyCampaignBlockBatch(ctx context.Context, command ApplyCampaignBlockBatchCommand) (emailcontent.CollaborationChange, error) {
	if command.Batch == nil {
		return emailcontent.CollaborationChange{}, domainerr.Required("batch")
	}
	write, err := s.readCampaignCollaboration(ctx, strings.TrimSpace(command.CampaignID), command.Locale, command.Batch.GetContributorMemberIds())
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	expectedValue := strings.TrimSpace(command.Batch.GetExpectedRevision())
	if expected, err := uuid.Parse(expectedValue); err != nil || expected == uuid.Nil || expected.String() != expectedValue {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("batch.expected_revision", "must be a canonical UUID")
	}
	batch, err := campaignContent.DecodeCollaborationBatch(write.documentID, write.locale, command.Batch, command.AffectedLocaleValues)
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	if write.locale != write.sourceLocale {
		originMemberID, err := write.requireTargetOrigin()
		if err != nil {
			return emailcontent.CollaborationChange{}, err
		}
		return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx Store) (emailcontent.CollaborationChange, error) {
			bound := tx
			return campaignContent.SaveCollaborationTarget(ctx, bound, emailcontent.TargetLocaleMutation{
				ID: write.campaignID, DocumentID: write.documentID, Locale: write.locale,
				Batch: batch, ExpectedDocumentRevision: batch.ExpectedRevision,
				ExpectedTargetRevision: command.ExpectedTargetRevision, Now: write.now,
				Fence: campaignEmailContentFence(bound, write.campaignID),
			}, originMemberID)
		})
	}
	if command.ExpectedTargetRevision != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx Store) (emailcontent.CollaborationChange, error) {
		result, err := tx.ApplyContentBatch(ctx, batch, campaignEmailContentFence(tx, write.campaignID))
		if err != nil {
			return emailcontent.CollaborationChange{}, campaignContent.NormalizeContentBlockError(err)
		}
		change := emailcontent.CollaborationChange{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: result.TranslationSourceChanged, ChangedLocales: result.ChangedLocales, Locale: write.locale,
			Record: mutation.Unchanged(),
		}
		if !result.Changed {
			return change, nil
		}
		// The source graph/revision advances independently of existing
		// target rows. Shared-presentation-only edits report no changed
		// locale but still require the source materialization to follow
		// the document; existing target rows remain unchanged.
		return change, s.projectCampaignSource(ctx, tx, write)
	})
}

// projectCampaignSource re-materializes the source locale from the locked
// document.
func (s *InternalService) projectCampaignSource(ctx context.Context, tx Store, write campaignCollaboration) error {
	domain, err := loadCampaignSourceContext(ctx, tx, write.campaignID, true)
	if err != nil {
		return err
	}
	snapshot, err := tx.LockedContentSnapshot(ctx, write.documentID, domain.SourceLocale)
	if err != nil {
		return campaignContent.NormalizeContentBlockError(err)
	}
	return projectCampaignEmailMaterializedContent(ctx, tx, write.campaignID, snapshot, []string{domain.SourceLocale}, write.now)
}

// UpdateCampaignLocaleMetadataCommand changes a Campaign's subject in one
// locale as the next document revision.
type UpdateCampaignLocaleMetadataCommand struct {
	CampaignID             string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	Subject                *string
	Contributors           []string
}

// UpdateCampaignLocaleMetadata stores the subject of a target locale, or of
// the source locale as a translation source change.
func (s *InternalService) UpdateCampaignLocaleMetadata(ctx context.Context, command UpdateCampaignLocaleMetadataCommand) (emailcontent.CollaborationChange, error) {
	campaignID := strings.TrimSpace(command.CampaignID)
	if campaignID == "" {
		return emailcontent.CollaborationChange{}, domainerr.Required("campaign_id")
	}
	locale, err := emailcontent.NormalizeLocale(command.Locale)
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	if command.Subject == nil {
		return emailcontent.CollaborationChange{}, domainerr.Required("subject")
	}
	expectedRevision, err := uuid.Parse(strings.TrimSpace(command.ExpectedRevision))
	if err != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_revision", "must be a UUID")
	}
	write, err := s.readCampaignCollaboration(ctx, campaignID, locale, command.Contributors)
	if err != nil {
		return emailcontent.CollaborationChange{}, err
	}
	if write.locale != write.sourceLocale {
		originMemberID, err := write.requireTargetOrigin()
		if err != nil {
			return emailcontent.CollaborationChange{}, err
		}
		return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx Store) (emailcontent.CollaborationChange, error) {
			bound := tx
			return campaignContent.SaveCollaborationTarget(ctx, bound, emailcontent.TargetLocaleMutation{
				ID: write.campaignID, DocumentID: write.documentID, Locale: write.locale,
				Batch:                    contentblock.Batch{DocumentID: write.documentID, ExpectedRevision: expectedRevision},
				ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: command.ExpectedTargetRevision,
				SetSubject: true, Subject: command.Subject, Now: write.now,
				Fence: campaignEmailContentFence(bound, write.campaignID),
			}, originMemberID)
		})
	}
	if command.ExpectedTargetRevision != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx Store) (emailcontent.CollaborationChange, error) {
		return s.saveCampaignSourceSubject(ctx, tx, write, expectedRevision, *command.Subject)
	})
}

// saveCampaignSourceSubject stores the source subject as the next document
// revision and refreshes the source materialization when the translation
// source changed.
func (s *InternalService) saveCampaignSourceSubject(ctx context.Context, tx Store, write campaignCollaboration, expectedRevision uuid.UUID, subject string) (emailcontent.CollaborationChange, error) {
	var sourceLocale string
	result, err := tx.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: write.documentID, ExpectedRevision: expectedRevision},
		campaignEmailContentFence(tx, write.campaignID),
		func(ctx context.Context) (contentblock.MetadataEffect, error) {
			domain, err := loadCampaignSourceContext(ctx, tx, write.campaignID, true)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			sourceLocale = domain.SourceLocale
			return updateCampaignEmailLocaleSubject(ctx, tx, write.campaignID, sourceLocale, subject, write.now)
		},
	)
	if err != nil {
		return emailcontent.CollaborationChange{}, campaignContent.NormalizeContentBlockError(err)
	}
	change := emailcontent.CollaborationChange{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged: result.TranslationSourceChanged, ChangedLocales: []string{sourceLocale}, Locale: write.locale,
		Record: mutation.Unchanged(),
	}
	if !result.TranslationSourceChanged {
		return change, nil
	}
	return change, s.projectCampaignSource(ctx, tx, write)
}

// LoadCampaignDocumentQuery reads a Campaign's document in one locale for a
// collaboration principal.
type LoadCampaignDocumentQuery struct {
	CampaignID string
	Locale     string
	Principal  *CollaborationPrincipal
}

// LoadCampaignDocument returns the Campaign's document in the locale after
// authorizing the room's principal to edit it.
func (s *InternalService) LoadCampaignDocument(ctx context.Context, query LoadCampaignDocumentQuery) (emailcontent.CollaborationDocument, error) {
	campaignID := strings.TrimSpace(query.CampaignID)
	if campaignID == "" {
		return emailcontent.CollaborationDocument{}, domainerr.Required("campaign_id")
	}
	locale, err := emailcontent.NormalizeLocale(query.Locale)
	if err != nil {
		return emailcontent.CollaborationDocument{}, err
	}
	if err := s.units.Read(ctx, func(store Store) error {
		return requireCampaignEmailDocumentLoad(ctx, store, s.permissions, query.Principal, campaignID)
	}); err != nil {
		return emailcontent.CollaborationDocument{}, err
	}
	return campaignContent.LoadCollaborationDocument(ctx, s.units, campaignID, locale, func(ctx context.Context, store Store) (uuid.UUID, error) {
		return loadCampaignEmailContentDocumentRoot(ctx, store, campaignID, true)
	})
}

// requireCampaignContributors locks the sorted, unique Members a
// collaboration write credits.
func requireCampaignContributors(ctx context.Context, tx Store, contributors []string) error {
	if len(contributors) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	for index, contributor := range contributors {
		if _, err := uuidutil.ParseCanonical(contributor, "contributor_member_ids"); err != nil {
			return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires canonical Member UUIDs")
		}
		if index > 0 && contributors[index-1] >= contributor {
			return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires sorted unique Member UUIDs")
		}
	}
	locked, err := tx.LockMembers(ctx, contributors)
	if err != nil {
		return fmt.Errorf("lock Campaign contributor Members: %w", err)
	}
	if len(locked) != len(contributors) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}
