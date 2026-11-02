package public

import (
	"context"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// hydrateAuthorizedContentBlockMedia adds request-scoped delivery and download
// actions to references loaded in the owning domain's document snapshot. This
// relation lock is held through download signing so replacement, removal, or a
// policy reset cannot commit and then receive a newly issued URL for stale data.
func (s *Service) hydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*ContentMediaItem,
) ([]*ContentMediaItem, error) {
	if len(items) == 0 {
		return items, nil
	}

	targets := contentMediaTargets(items)
	request := contentDownloadRequest{
		user:      auth.GetUser(ctx),
		selectors: make([]mediaasset.ContentBlockDownloadSelector, 0, len(targets)),
	}
	request.ownerAuthorization, request.ownerAuthorizationPresent = mediaasset.ContentDownloadOwnerAuthorizationFromContext(ctx)
	for _, target := range targets {
		request.selectors = append(request.selectors, target.selector)
	}

	var evaluation downloadEvaluation
	err := s.units.ReadLocked(ctx, func(tx Store) error {
		var err error
		evaluation, err = s.evaluateContentDownloads(ctx, tx, request)
		return err
	})
	if err != nil {
		return nil, err
	}

	err = s.units.ReadLocked(ctx, func(tx Store) error {
		return s.revalidateContentDownloads(ctx, tx, request, targets, evaluation)
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// contentMediaTarget is an item that references an active file through a
// content Block selector. Other items are left as loaded.
type contentMediaTarget struct {
	item     *ContentMediaItem
	fileID   string
	selector mediaasset.ContentBlockDownloadSelector
	key      string
}

func contentMediaTargets(items []*ContentMediaItem) []contentMediaTarget {
	targets := make([]contentMediaTarget, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		fileID := strings.TrimSpace(item.FileID)
		selector := item.Selector
		if fileID == "" || selector == nil {
			continue
		}
		targets = append(targets, contentMediaTarget{
			item:   item,
			fileID: fileID,
			selector: mediaasset.ContentBlockDownloadSelector{
				BlockID: selector.BlockID, ReferencePath: selector.ReferencePath,
			},
			key: mediaasset.ContentBlockDownloadPolicyKey(selector.BlockID, selector.ReferencePath),
		})
	}
	return targets
}

type contentDownloadRequest struct {
	user                      *auth.UserInfo
	ownerAuthorization        mediaasset.ContentDownloadOwnerAuthorization
	ownerAuthorizationPresent bool
	selectors                 []mediaasset.ContentBlockDownloadSelector
}

// contentDownloadFacts is what one pass observed about the selected
// references; the locked pass reuses a decision only while they are unchanged.
// Maps are keyed by content Block download policy key, owners by Block ID.
type contentDownloadFacts struct {
	ownerAuthorized   bool
	owners            map[string]OwnerWitness
	relations         map[string]string
	sources           map[string]mediaasset.FileDownloadSource
	fingerprints      map[string]string
	viewerFingerprint string
}

// downloadEvaluation is the unlocked pass: its facts and the download
// decision it made for each source.
type downloadEvaluation struct {
	contentDownloadFacts
	decisions map[string]*DownloadAccess
}

func (s *Service) evaluateContentDownloads(
	ctx context.Context,
	tx Store,
	request contentDownloadRequest,
) (downloadEvaluation, error) {
	var evaluation downloadEvaluation
	var err error
	evaluation.owners, err = tx.ContentOwners(ctx, request.selectors)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.ownerAuthorized, err = s.validateContentDownloadOwnerAuthorization(
		ctx, tx, request.ownerAuthorization, request.ownerAuthorizationPresent, evaluation.owners, false,
	)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.relations, err = tx.ContentRelations(ctx, request.selectors, false)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.sources, err = mediaasset.LoadContentBlockDownloadSources(ctx, tx, request.selectors)
	if err != nil {
		return downloadEvaluation{}, err
	}
	allowed, err := mediaasset.EvaluateFileDownloadAccessBatch(ctx, tx, s.spiceDB, evaluation.sources, request.user)
	if err != nil {
		return downloadEvaluation{}, err
	}
	signIn, err := contentDownloadSignInHints(ctx, tx, evaluation.sources, request.user)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.fingerprints, err = mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, evaluation.sources)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.viewerFingerprint, err = mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, evaluation.sources, request.user)
	if err != nil {
		return downloadEvaluation{}, err
	}
	evaluation.decisions = make(map[string]*DownloadAccess, len(request.selectors))
	for key, source := range evaluation.sources {
		if evaluation.ownerAuthorized {
			evaluation.decisions[key] = effectiveFileDownloadAccess(source.Audience, request.user, allowed[key], signIn[key])
		} else {
			evaluation.decisions[key] = unavailableFileDownloadAccess()
		}
	}
	return evaluation, nil
}

// contentDownloadSignInHints reports, for a reader without a member session,
// which sources signing in could open: authenticated-audience files and
// restricted files whose segments exist.
func contentDownloadSignInHints(
	ctx context.Context,
	tx Store,
	sources map[string]mediaasset.FileDownloadSource,
	user *auth.UserInfo,
) (map[string]bool, error) {
	signIn := make(map[string]bool, len(sources))
	if user != nil && user.Authenticated && user.MemberID != "" {
		return signIn, nil
	}
	restricted := make(map[string]mediaasset.FileDownloadSource)
	for key, source := range sources {
		switch source.Audience {
		case mediaasset.FileDownloadAudienceAuthenticated:
			signIn[key] = true
		case mediaasset.FileDownloadAudienceRestricted:
			restricted[key] = source
		}
	}
	presence, err := mediaasset.RestrictedFileDownloadSegmentPresence(ctx, tx, restricted)
	if err != nil {
		return nil, err
	}
	for key, present := range presence {
		signIn[key] = present
	}
	return signIn, nil
}

// lockedContentDownloads is the locked pass's facts plus whether the viewer's
// account facts are still active.
type lockedContentDownloads struct {
	contentDownloadFacts
	viewerActive bool
}

// revalidateContentDownloads locks what the evaluation depended on, reloads
// it, and applies a delivery and download decision to each target. A target
// whose relation changed only gets the unavailable decision.
func (s *Service) revalidateContentDownloads(
	ctx context.Context,
	tx Store,
	request contentDownloadRequest,
	targets []contentMediaTarget,
	evaluation downloadEvaluation,
) error {
	current, err := s.lockContentDownloads(ctx, tx, request, evaluation)
	if err != nil {
		return err
	}
	decisions := make(map[string]*DownloadAccess, len(targets))
	deliveryAccess := make(map[string]resolvedMediaAccess, len(targets))
	stableRelations := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		decision, stable := reusableDecision(target, evaluation, current)
		decisions[target.key] = decision
		if stable {
			stableRelations[target.key] = struct{}{}
			deliveryAccess[target.fileID] = resolvedMediaAccess{}
		}
	}
	deliveries, err := s.buildScopedMediaURLs(ctx, tx, deliveryAccess)
	if err != nil {
		return err
	}
	for _, target := range targets {
		decision := decisions[target.key]
		if decision == nil {
			decision = unavailableFileDownloadAccess()
		}
		if _, stable := stableRelations[target.key]; !stable {
			applyContentBlockDownloadDecision(target.item, decision)
			continue
		}
		if err := s.applyDelivery(target.item, decision, deliveries[target.fileID], current.sources[target.key]); err != nil {
			return err
		}
	}
	return nil
}

// lockContentDownloads takes the policy, owner and viewer locks in the order
// every download path uses, then reloads the facts the evaluation used.
func (s *Service) lockContentDownloads(
	ctx context.Context,
	tx Store,
	request contentDownloadRequest,
	evaluation downloadEvaluation,
) (lockedContentDownloads, error) {
	var current lockedContentDownloads
	if err := mediaasset.LockFileDownloadPolicySegments(ctx, tx, evaluation.sources); err != nil {
		return current, err
	}
	var err error
	current.owners, err = tx.LockContentOwners(ctx, evaluation.owners)
	if err != nil {
		return current, err
	}
	current.ownerAuthorized, err = s.validateContentDownloadOwnerAuthorization(
		ctx, tx, request.ownerAuthorization, request.ownerAuthorizationPresent, current.owners, true,
	)
	if err != nil {
		return current, err
	}
	current.viewerActive, err = mediaasset.LockFileDownloadViewerFacts(ctx, tx, evaluation.sources, request.user)
	if err != nil {
		return current, err
	}
	current.relations, err = tx.ContentRelations(ctx, request.selectors, true)
	if err != nil {
		return current, err
	}
	current.sources, err = mediaasset.LoadContentBlockDownloadSources(ctx, tx, request.selectors)
	if err != nil {
		return current, err
	}
	current.fingerprints, err = mediaasset.LoadFileDownloadPolicyFingerprints(ctx, tx, current.sources)
	if err != nil {
		return current, err
	}
	current.viewerFingerprint, err = mediaasset.LoadFileDownloadViewerFingerprint(ctx, tx, current.sources, request.user)
	if err != nil {
		return current, err
	}
	return current, nil
}

// reusableDecision returns the evaluated decision for target when the locked
// pass still sees the same owner, relation, source and policy, and the
// unavailable decision otherwise. stable reports whether the owner and
// relation held, which is what lets the target receive a delivery.
func reusableDecision(
	target contentMediaTarget,
	evaluated downloadEvaluation,
	current lockedContentDownloads,
) (decision *DownloadAccess, stable bool) {
	blockID, key := target.selector.BlockID, target.key
	evaluatedOwner, evaluatedOwnerExists := evaluated.owners[blockID]
	currentOwner, currentOwnerExists := current.owners[blockID]
	evaluatedRelationFileID, evaluatedRelationExists := evaluated.relations[key]
	currentRelationFileID, currentRelationExists := current.relations[key]
	stable = evaluated.ownerAuthorized && current.ownerAuthorized &&
		evaluatedRelationExists && currentRelationExists &&
		evaluatedOwnerExists && currentOwnerExists && evaluatedOwner == currentOwner &&
		evaluatedRelationFileID == target.fileID && currentRelationFileID == target.fileID
	if !stable {
		return unavailableFileDownloadAccess(), false
	}
	source, exists := current.sources[key]
	evaluatedSource, evaluatedExists := evaluated.sources[key]
	if !exists || !evaluatedExists ||
		source.FileID != target.fileID || evaluatedSource.FileID != target.fileID ||
		current.fingerprints[key] != evaluated.fingerprints[key] {
		return unavailableFileDownloadAccess(), true
	}
	if fileDownloadDecisionDependsOnActiveViewer(evaluatedSource, evaluated.decisions[key]) &&
		(!current.viewerActive || current.viewerFingerprint != evaluated.viewerFingerprint) {
		return unavailableFileDownloadAccess(), true
	}
	return evaluated.decisions[key], true
}

// applyDelivery sets item's delivery, signing a download URL for source when
// the decision allows a download, and then applies the decision.
func (s *Service) applyDelivery(
	item *ContentMediaItem,
	decision *DownloadAccess,
	delivery *Delivery,
	source mediaasset.FileDownloadSource,
) error {
	if delivery != nil {
		item.Delivery = delivery.Clone()
		if downloadAction(decision) == ActionDownload {
			download, err := mediaauth.ExpiringReference(
				s.mediaDomain, s.mediaSecret, source.FileID, source.Extension,
				source.MimeType, source.FileName, mediaauth.PurposeDownload, s.effectiveDownloadTTL(),
			)
			if err != nil {
				return err
			}
			item.Delivery.Download = download
		}
	}
	applyContentBlockDownloadDecision(item, decision)
	return nil
}

func (s *Service) validateContentDownloadOwnerAuthorization(
	ctx context.Context,
	store Store,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
	present bool,
	owners map[string]OwnerWitness,
	locked bool,
) (bool, error) {
	if !present || len(owners) == 0 {
		return false, nil
	}
	for _, owner := range owners {
		if authorization.ResourceType != owner.ResourceType || authorization.ResourceID != owner.ResourceID ||
			authorization.Status != owner.Status || authorization.DocumentID != owner.DocumentID {
			return false, nil
		}
	}
	switch authorization.Mode {
	case mediaasset.ContentDownloadOwnerAccessPublic:
		if !contentDownloadOwnerStatusIsPublic(authorization.ResourceType, authorization.Status) {
			return false, nil
		}
		if authorization.ResourceType == "page" {
			reason, err := store.PageAccess(ctx, s.spiceDB, authorization.ResourceID)
			return reason == pageaccess.ReasonAllowed, err
		}
		return true, nil
	case mediaasset.ContentDownloadOwnerAccessAuthenticatedDraft:
		user := auth.GetUser(ctx)
		if user == nil || !user.Authenticated || user.Banned ||
			user.IdentityID.String() != authorization.IdentityID || user.MemberID.String() != authorization.MemberID {
			return false, nil
		}
		if locked {
			return lockContentDownloadOwnerPrincipal(ctx, store, authorization)
		}
		var action auth.ResourceAction
		switch authorization.ResourceType {
		case "post":
			action = policyv1.Post.View
		case "page":
			action = policyv1.Page.View
		case "work":
			action = policyv1.Work.View
		case "program_event":
			action = policyv1.ProgramEvent.View
		default:
			return false, nil
		}
		return hasDraftResourceView(ctx, s.spiceDB, action, authorization.ResourceID)
	case mediaasset.ContentDownloadOwnerAccessShare:
		return validateContentDownloadShareLinkWitness(ctx, store, authorization, locked)
	default:
		return false, nil
	}
}

func contentDownloadOwnerStatusIsPublic(resourceType, status string) bool {
	switch resourceType {
	case "post":
		return status == ownerPostPublished || status == ownerPostArchived
	case "page":
		return status == ownerPagePublished
	case "work":
		return status == ownerWorkPublished || status == ownerWorkArchived
	case "program_event":
		return status == ownerProgramEventPublished || status == ownerProgramEventArchived
	default:
		return false
	}
}

func lockContentDownloadOwnerPrincipal(
	ctx context.Context,
	store Store,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
) (bool, error) {
	principal := &auth.UserInfo{
		IdentityID:    auth.IdentityID(authorization.IdentityID),
		MemberID:      auth.MemberID(authorization.MemberID),
		Authenticated: true, Onboarded: true,
	}
	return store.LockActivePrincipal(ctx, principal)
}

func validateContentDownloadShareLinkWitness(
	ctx context.Context,
	store Store,
	authorization mediaasset.ContentDownloadOwnerAuthorization,
	locked bool,
) (bool, error) {
	witness := authorization.ShareLink
	expectedEntityType := contentDownloadShareLinkEntityType(authorization.ResourceType)
	if witness == nil || witness.ID == "" || expectedEntityType == "" || witness.EntityType != expectedEntityType || witness.EntityID != authorization.ResourceID {
		return false, nil
	}
	link, found, err := store.ShareLink(ctx, witness.ID, locked)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}

	if link.ID != witness.ID || link.EntityType != witness.EntityType || link.EntityID != witness.EntityID ||
		!sameContentDownloadTime(link.ExpiresAt, witness.ExpiresAt) {
		return false, nil
	}
	return link.ExpiresAt != nil && link.ExpiresAt.After(time.Now()), nil
}

func contentDownloadShareLinkEntityType(resourceType string) string {
	switch resourceType {
	case "post":
		return "SHARE_LINK_ENTITY_TYPE_POST"
	case "page":
		return "SHARE_LINK_ENTITY_TYPE_PAGE"
	case "work":
		return "SHARE_LINK_ENTITY_TYPE_WORK"
	case "release":
		return "SHARE_LINK_ENTITY_TYPE_RELEASE"
	default:
		return ""
	}
}

func sameContentDownloadTime(left, right *time.Time) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && left.Equal(*right))
}

// HydrateContentMedia adds delivery to exact references only
// after the owning public domain has authorized and loaded its document.
func (s *Service) HydrateContentMedia(
	ctx context.Context,
	items []*ContentMediaItem,
) ([]*ContentMediaItem, error) {
	return s.hydrateAuthorizedContentBlockMedia(ctx, items)
}

func applyContentBlockDownloadDecision(item *ContentMediaItem, decision *DownloadAccess) {
	if item == nil || decision == nil {
		return
	}
	switch decision.Availability {
	case DownloadAvailable, DownloadUnavailable:
		item.DownloadAvailability = decision.Availability
	}
	switch decision.Action {
	case ActionDownload, ActionSignIn, ActionNone:
		item.DownloadAction = decision.Action
	}
}
