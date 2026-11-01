package campaign

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// InternalDeps are the collaborators of the Campaign collaboration and AI
// document use cases. Every field is required; NewInternalService rejects a
// missing one.
type InternalDeps struct {
	DB      *gorm.DB
	SpiceDB *auth.SpiceDBClient
	// Permissions answers the Edit decisions of room loads and AI document
	// writes.
	Permissions   CollaborationPermissionChecker
	ContentBlocks *contentblock.Store
	Checkpoints   persistencecheckpoint.ContributorFence
	Members       ContributorMembers
	Audit         domainaudit.Appender
}

func (d InternalDeps) validate() error {
	return requireDependencies("internal campaign", map[string]bool{
		"DB": d.DB == nil, "SpiceDB": d.SpiceDB == nil, "Permissions": d.Permissions == nil,
		"ContentBlocks": d.ContentBlocks == nil, "Checkpoints": d.Checkpoints == nil,
		"Members": d.Members == nil, "Audit": d.Audit == nil,
	})
}

// InternalService owns the typed Block persistence boundary for one
// Campaign document. The collaboration runtime transports operations; it
// does not own durable document bytes.
type InternalService struct {
	contentMutations *mutation.StoreRunner[contentStore]
	db               *gorm.DB
	permissions      CollaborationPermissionChecker
	contentBlocks    *contentblock.Store
	checkpoints      persistencecheckpoint.ContributorFence
	members          ContributorMembers
	mutations        *mutation.Runner
}

// NewInternalService validates deps and returns the Campaign collaboration
// and AI document use cases.
func NewInternalService(deps InternalDeps) (*InternalService, error) {
	if err := deps.validate(); err != nil {
		return nil, err
	}
	mutations, err := mutation.NewRunner(deps.DB, deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
	if err != nil {
		return nil, err
	}
	contentMutations, err := contentRunner(deps.DB, deps)
	if err != nil {
		return nil, err
	}
	return &InternalService{
		contentMutations: contentMutations,
		db:               deps.DB, permissions: deps.Permissions, contentBlocks: deps.ContentBlocks,
		checkpoints: deps.Checkpoints, members: deps.Members, mutations: mutations,
	}, nil
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
	write := campaignCollaboration{campaignID: campaignID, contributors: contributors, now: dbtime.Now()}
	if write.campaignID == "" {
		return write, domainerr.Required("campaign_id")
	}
	var err error
	if write.locale, err = emailcontent.NormalizeLocale(locale); err != nil {
		return write, err
	}
	if write.documentID, err = loadCampaignEmailContentDocumentRoot(ctx, s.db, campaignID, false); err != nil {
		return write, err
	}
	domain, err := loadCampaignSourceContext(ctx, s.db, campaignID, false)
	write.sourceLocale = domain.SourceLocale
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
	change func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error),
) (emailcontent.CollaborationChange, error) {
	return mutation.Run(ctx, s.mutations, mutation.Spec[emailcontent.CollaborationChange]{
		Access: emailcontent.CollaborationAccess,
		Change: func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error) {
			if err := requireCampaignContributors(ctx, tx, s.members, write.contributors); err != nil {
				return emailcontent.CollaborationChange{}, err
			}
			if err := s.checkpoints.RequireCurrentContributors(
				ctx, tx, intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN, write.campaignID, write.contributors,
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
		return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error) {
			bound := bindContent(tx, s.contentBlocks)
			return campaignContent.SaveCollaborationTarget(ctx, bound, emailcontent.TargetLocaleMutation{
				ID: write.campaignID, DocumentID: write.documentID, Locale: write.locale,
				Batch: batch, ExpectedDocumentRevision: batch.ExpectedRevision,
				ExpectedTargetRevision: command.ExpectedTargetRevision, Now: write.now,
				Fence: contentFence(bound, campaignEmailContentFence(write.campaignID)),
			}, originMemberID)
		})
	}
	if command.ExpectedTargetRevision != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error) {
		result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, campaignEmailContentFence(write.campaignID))
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
func (s *InternalService) projectCampaignSource(ctx context.Context, tx *gorm.DB, write campaignCollaboration) error {
	domain, err := loadCampaignSourceContext(ctx, tx, write.campaignID, true)
	if err != nil {
		return err
	}
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, write.documentID, domain.SourceLocale)
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
		return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error) {
			bound := bindContent(tx, s.contentBlocks)
			return campaignContent.SaveCollaborationTarget(ctx, bound, emailcontent.TargetLocaleMutation{
				ID: write.campaignID, DocumentID: write.documentID, Locale: write.locale,
				Batch:                    contentblock.Batch{DocumentID: write.documentID, ExpectedRevision: expectedRevision},
				ExpectedDocumentRevision: expectedRevision, ExpectedTargetRevision: command.ExpectedTargetRevision,
				SetSubject: true, Subject: command.Subject, Now: write.now,
				Fence: contentFence(bound, campaignEmailContentFence(write.campaignID)),
			}, originMemberID)
		})
	}
	if command.ExpectedTargetRevision != nil {
		return emailcontent.CollaborationChange{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	return s.saveCampaignCollaboration(ctx, write, func(ctx context.Context, tx *gorm.DB) (emailcontent.CollaborationChange, error) {
		return s.saveCampaignSourceSubject(ctx, tx, write, expectedRevision, *command.Subject)
	})
}

// saveCampaignSourceSubject stores the source subject as the next document
// revision and refreshes the source materialization when the translation
// source changed.
func (s *InternalService) saveCampaignSourceSubject(ctx context.Context, tx *gorm.DB, write campaignCollaboration, expectedRevision uuid.UUID, subject string) (emailcontent.CollaborationChange, error) {
	var sourceLocale string
	result, err := s.contentBlocks.AdvanceRevision(
		ctx, tx,
		contentblock.AdvanceInput{DocumentID: write.documentID, ExpectedRevision: expectedRevision},
		campaignEmailContentFence(write.campaignID),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
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
	Principal  *intrav1.CollaborationPrincipal
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
	if err := requireCampaignEmailDocumentLoad(ctx, s.db, s.permissions, query.Principal, campaignID); err != nil {
		return emailcontent.CollaborationDocument{}, err
	}
	return campaignContent.LoadCollaborationDocument(ctx, contentUnits(s.db, s.contentBlocks), campaignID, locale, func(ctx context.Context, store contentStore) (uuid.UUID, error) {
		return loadCampaignEmailContentDocumentRoot(ctx, store.tx, campaignID, true)
	})
}

// requireCampaignContributors locks the sorted, unique Members a
// collaboration write credits.
func requireCampaignContributors(ctx context.Context, tx *gorm.DB, members ContributorMembers, contributors []string) error {
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
	locked, err := members.LockMembers(ctx, tx, contributors)
	if err != nil {
		return fmt.Errorf("lock Campaign contributor Members: %w", err)
	}
	if len(locked) != len(contributors) {
		return domainerr.InvalidField("contributor_member_ids", "contains a Member that does not exist")
	}
	return nil
}
