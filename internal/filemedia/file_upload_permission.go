package filemedia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type uploadPermissionTarget struct {
	resourceType         string
	requiresSpiceDBCheck bool
	adminOnly            bool
	authorOnly           bool
	userOwned            bool
}

var errUnsupportedUploadAuthorizationResource = errors.New("unsupported upload authorization resource")

type uploadPermissionValidationError struct {
	field    string
	message  string
	required bool
}

func (e *uploadPermissionValidationError) Error() string {
	return e.message
}

func uploadTypeToResourceType(uploadType UploadType) (resourceType string, requiresSpiceDBCheck bool) {
	switch uploadType {
	case UploadArtistImage:
		return "artist", true
	case UploadFeaturedImage:
		return "post", true
	case UploadWorkFeaturedImage:
		return "work", true
	case UploadSeriesFeaturedImage:
		return "series", true
	case UploadFormFeaturedImage:
		return "form", true
	case UploadProgramEventPoster:
		return "", false
	case UploadReleaseArtwork, UploadTrackAudio:
		return "release", false
	case UploadLabelImage:
		return "label", true
	case UploadEditorImage,
		UploadEditorVideo,
		UploadEditorAudio,
		UploadEditorAttachment,
		UploadEditorMesh:
		return "", false
	case UploadUserAvatar:
		return "user", false
	case UploadSiteLogo,
		UploadSiteFavicon,
		UploadSiteLoader,
		UploadSiteOGBackground,
		UploadClientLogo:
		return "", false
	default:
		return "", false
	}
}

func resolveUploadPermissionTarget(uploadType UploadType, entityType string) (uploadPermissionTarget, error) {
	resourceType, requiresSpiceDBCheck := uploadTypeToResourceType(uploadType)
	target := uploadPermissionTarget{
		resourceType:         resourceType,
		requiresSpiceDBCheck: requiresSpiceDBCheck,
	}

	switch uploadType {
	case UploadGeneralFile:
		target.authorOnly = true
	case UploadUserAvatar:
		target.userOwned = true
	case UploadFeaturedImage:
		if entityType == EntityPage.String() {
			target.resourceType = "page"
			target.requiresSpiceDBCheck = true
		}
	case UploadSiteLogo,
		UploadSiteFavicon,
		UploadSiteLoader,
		UploadSiteOGBackground,
		UploadClientLogo,
		UploadMapImage:
		target.adminOnly = true
	case UploadReleaseArtwork,
		UploadTrackAudio:
		target.adminOnly = true
	case UploadProgramEventPoster:
		target.adminOnly = true
	case UploadEditorImage,
		UploadEditorVideo,
		UploadEditorAudio,
		UploadEditorAttachment,
		UploadEditorMesh:
		if strings.TrimSpace(entityType) != "" {
			return target, &uploadPermissionValidationError{
				field:   "entity_type",
				message: "editor File upload must omit document entity type",
			}
		}
		target.authorOnly = true
	}

	if target.resourceType == "page" || target.resourceType == "work" ||
		uploadType == UploadFormFeaturedImage {
		target.adminOnly = true
	}
	return target, nil
}

func uploadPermissionRequiresEntityID(uploadType UploadType, target uploadPermissionTarget) bool {
	if target.resourceType != "" || target.requiresSpiceDBCheck || target.userOwned {
		return true
	}
	return uploadType == UploadClientLogo
}

func connectUploadPermissionValidationError(err error) error {
	var validationErr *uploadPermissionValidationError
	if errors.As(err, &validationErr) {
		if validationErr.required {
			return domainerr.Required(validationErr.field)
		}
		return domainerr.InvalidField(validationErr.field, validationErr.message)
	}
	return err
}

func uploadPermissionPartError(err error) error {
	var validationErr *uploadPermissionValidationError
	if errors.As(err, &validationErr) {
		return errors.New(validationErr.message)
	}
	return err
}

func (s *Service) resolvePartUploadPermissionTarget(
	ctx context.Context,
	uploadType UploadType,
	session domain.UploadSession,
) (uploadPermissionTarget, string, error) {
	return resolvePartUploadPermissionTargetUsing(ctx, uploadType, session, s.resolveTrackReleaseID)
}

func resolvePartUploadPermissionTargetUsing(ctx context.Context, uploadType UploadType, session domain.UploadSession, lookup func(context.Context, string) (string, error)) (uploadPermissionTarget, string, error) {
	entityType := ""
	if session.EntityType != nil {
		entityType = *session.EntityType
	}
	target, err := resolveUploadPermissionTarget(uploadType, entityType)
	if err != nil {
		return target, "", uploadPermissionPartError(err)
	}
	entityID := strings.TrimSpace(session.EntityID)
	if uploadPermissionRequiresEntityID(uploadType, target) && entityID == "" {
		return target, "", fmt.Errorf("entity_id is required")
	}
	if uploadType != UploadTrackAudio {
		return target, entityID, nil
	}
	releaseID, err := lookup(ctx, session.EntityID)
	if errors.Is(err, domain.ErrUploadTrackNotFound) {
		return target, "", fmt.Errorf("track not found")
	}
	if err != nil {
		return target, "", fmt.Errorf("failed to resolve track release: %w", err)
	}
	return target, releaseID, nil
}

func (s *Service) validatePartUploadTarget(
	ctx context.Context,
	uploadType UploadType,
	target uploadPermissionTarget,
	entityID string,
) error {
	if uploadPermissionRequiresEntityID(uploadType, target) && strings.TrimSpace(entityID) == "" {
		return fmt.Errorf("entity_id is required")
	}
	if target.resourceType != "post" && target.resourceType != "program_event" {
		if err := s.ensureUploadPermissionEntityExists(ctx, target, entityID); err != nil {
			return err
		}
	}
	if target.resourceType == "work" {
		return s.readUploadFacts(ctx, func(store Store) error { return store.RequireUploadWork(ctx, entityID) })
	}
	return nil
}

func (s *Service) checkMemberAuthorityUploadTarget(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	userID string,
) (bool, error) {
	// Resource ownership is evaluated by the typed SpiceDB relation graph at
	// the final part-upload boundary. Do not use the database authority
	// projection as a second authorization source.
	return false, nil
}

func (s *Service) checkRoleAndSpiceDBUploadPermission(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	userID string,
) error {
	if target.resourceType == "post" {
		return s.requireOwnerPreflight(ctx, "post", entityID, true)
	}
	if target.resourceType == "program_event" {
		return s.requireOwnerPreflight(ctx, "program_event", entityID, true)
	}
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return fmt.Errorf("admin role check failed: %w", err)
	}
	isAdmin, err := checkSpiceDBCan(ctx, auth.GetUser(ctx), can, s.spiceDB)
	if err != nil {
		return fmt.Errorf("admin role check failed: %w", err)
	}
	if isAdmin {
		return nil
	}
	if err := s.requireUploadRole(ctx, target); err != nil {
		return err
	}
	if target.adminOnly {
		return fmt.Errorf("admin access required for this upload type")
	}
	if target.userOwned {
		if entityID != userID {
			return fmt.Errorf("cannot upload avatar for another user")
		}
		return nil
	}
	return s.checkSpiceDBEntityUploadPermission(ctx, target, entityID)
}

func (s *Service) requireUploadRole(
	ctx context.Context,
	target uploadPermissionTarget,
) error {
	if !target.authorOnly {
		return nil
	}
	can, err := policyv1.File.List()
	if err != nil {
		return fmt.Errorf("author role check failed: %w", err)
	}
	isAuthor, err := checkSpiceDBCan(ctx, auth.GetUser(ctx), can, s.spiceDB)
	if err != nil {
		return fmt.Errorf("author role check failed: %w", err)
	}
	if !isAuthor {
		return fmt.Errorf("file manager upload requires author role")
	}
	return nil
}

func (s *Service) checkSpiceDBEntityUploadPermission(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
) error {
	if !target.requiresSpiceDBCheck || target.resourceType == "" || entityID == "" {
		return nil
	}
	if err := requireUploadIdentity(ctx); err != nil {
		return err
	}
	can, err := uploadPermissionEditCan(target.resourceType, entityID)
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return err
	}
	hasPermission, err := s.spiceDB.Can(ctx, decision)
	if err != nil {
		return failurecause.WithAttrs(
			fmt.Errorf("permission check failed: %w", err),
			slog.String("resource_type", target.resourceType), slog.String("entity_id", entityID),
		)
	}
	if !hasPermission {
		return fmt.Errorf("no permission to upload to this entity")
	}
	return nil
}

func uploadPermissionEditCan(resourceType, resourceID string) (policyv1.Can, error) {
	switch resourceType {
	case "artist":
		return policyv1.Artist.Edit(resourceID)
	case "form":
		return policyv1.Form.Edit(resourceID)
	case "label":
		return policyv1.Label.Edit(resourceID)
	case "page":
		return policyv1.Page.Edit(resourceID)
	case "post":
		return policyv1.Post.Edit(resourceID)
	case "program_event":
		return policyv1.ProgramEvent.Edit(resourceID)
	case "release":
		return policyv1.Release.Edit(resourceID)
	case "series":
		return policyv1.PostSeries.Edit(resourceID)
	case "work":
		return policyv1.Work.Edit(resourceID)
	default:
		return policyv1.Can{}, fmt.Errorf("%w %q", errUnsupportedUploadAuthorizationResource, resourceType)
	}
}

func requireUploadIdentity(ctx context.Context) error {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.IdentityID.String()) == "" {
		return domainerr.AuthenticationRequired()
	}
	return nil
}

func checkSpiceDBCan(
	ctx context.Context,
	user *auth.UserInfo,
	can policyv1.Can,
	spiceDB authz.AuthorizationDecisionChecker,
) (bool, error) {
	if user == nil || spiceDB == nil {
		return false, fmt.Errorf("SpiceDB authorization is not configured")
	}
	decision, err := auth.AuthorizationDecision(auth.WithUser(ctx, user), can)
	if err != nil {
		return false, err
	}
	return spiceDB.Can(ctx, decision)
}
