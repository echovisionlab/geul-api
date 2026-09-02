package filemedia

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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

func uploadTypeToResourceType(uploadType managev1.UploadType) (resourceType string, requiresSpiceDBCheck bool) {
	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_ARTIST_IMAGE:
		return "artist", true
	case managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE:
		return "post", true
	case managev1.UploadType_UPLOAD_TYPE_WORK_FEATURED_IMAGE:
		return "work", true
	case managev1.UploadType_UPLOAD_TYPE_SERIES_FEATURED_IMAGE:
		return "series", true
	case managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE:
		return "form", true
	case managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER:
		return "", false
	case managev1.UploadType_UPLOAD_TYPE_RELEASE_ARTWORK, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO:
		return "release", false
	case managev1.UploadType_UPLOAD_TYPE_LABEL_IMAGE:
		return "label", true
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH:
		return "", false
	case managev1.UploadType_UPLOAD_TYPE_USER_AVATAR:
		return "user", false
	case managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
		managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON,
		managev1.UploadType_UPLOAD_TYPE_SITE_LOADER,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
		managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO:
		return "", false
	default:
		return "", false
	}
}

func resolveUploadPermissionTarget(uploadType managev1.UploadType, entityType string) (uploadPermissionTarget, error) {
	resourceType, requiresSpiceDBCheck := uploadTypeToResourceType(uploadType)
	target := uploadPermissionTarget{
		resourceType:         resourceType,
		requiresSpiceDBCheck: requiresSpiceDBCheck,
	}

	switch uploadType {
	case managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE:
		target.authorOnly = true
	case managev1.UploadType_UPLOAD_TYPE_USER_AVATAR:
		target.userOwned = true
	case managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE:
		if entityType == managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE.String() {
			target.resourceType = "page"
			target.requiresSpiceDBCheck = true
		}
	case managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
		managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON,
		managev1.UploadType_UPLOAD_TYPE_SITE_LOADER,
		managev1.UploadType_UPLOAD_TYPE_SITE_OG_BACKGROUND,
		managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO,
		managev1.UploadType_UPLOAD_TYPE_MAP_IMAGE:
		target.adminOnly = true
	case managev1.UploadType_UPLOAD_TYPE_RELEASE_ARTWORK,
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO:
		target.adminOnly = true
	case managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER:
		target.adminOnly = true
	case managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
		managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH:
		if strings.TrimSpace(entityType) != "" {
			return target, &uploadPermissionValidationError{
				field:   "entity_type",
				message: "editor File upload must omit document entity type",
			}
		}
		target.authorOnly = true
	}

	if target.resourceType == "page" || target.resourceType == "work" ||
		uploadType == managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE {
		target.adminOnly = true
	}
	return target, nil
}

func uploadPermissionRequiresEntityID(uploadType managev1.UploadType, target uploadPermissionTarget) bool {
	if target.resourceType != "" || target.requiresSpiceDBCheck || target.userOwned {
		return true
	}
	return uploadType == managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO
}

func connectUploadPermissionValidationError(err error) error {
	var validationErr *uploadPermissionValidationError
	if errors.As(err, &validationErr) {
		if validationErr.required {
			return errs.Required(validationErr.field)
		}
		return errs.InvalidArgument(validationErr.field, validationErr.message)
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

func (s *FileService) resolvePartUploadPermissionTarget(
	ctx context.Context,
	uploadType managev1.UploadType,
	session model.UploadSession,
) (uploadPermissionTarget, string, error) {
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
	if uploadType != managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO {
		return target, entityID, nil
	}
	releaseID, err := s.resolveTrackReleaseID(ctx, session.EntityID)
	if err == gorm.ErrRecordNotFound {
		return target, "", fmt.Errorf("track not found")
	}
	if err != nil {
		return target, "", fmt.Errorf("failed to resolve track release: %w", err)
	}
	return target, releaseID, nil
}

func (s *FileService) validatePartUploadTarget(
	ctx context.Context,
	uploadType managev1.UploadType,
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
		attachment, err := requireWorkAttachment(s.workAttachment)
		if err != nil {
			return err
		}
		return attachment.RequireExists(ctx, entityID)
	}
	return nil
}

func (s *FileService) checkMemberAuthorityUploadTarget(
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

func (s *FileService) checkRoleAndSpiceDBUploadPermission(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	userID string,
) error {
	if target.resourceType == "post" {
		access, err := requirePostAccess(s.postAccess)
		if err != nil {
			return err
		}
		return access.RequireEdit(ctx, entityID)
	}
	if target.resourceType == "program_event" {
		access, err := requireProgramEventAttachment(s.programEventAttachment)
		if err != nil {
			return err
		}
		return access.RequireEdit(ctx, s.spiceDB, entityID)
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
	if err := s.requireUploadRole(ctx, target, userID); err != nil {
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
	return s.checkSpiceDBEntityUploadPermission(ctx, target, entityID, userID)
}

func (s *FileService) requireUploadRole(
	ctx context.Context,
	target uploadPermissionTarget,
	userID string,
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

func (s *FileService) checkSpiceDBEntityUploadPermission(
	ctx context.Context,
	target uploadPermissionTarget,
	entityID string,
	userID string,
) error {
	if !target.requiresSpiceDBCheck || target.resourceType == "" || entityID == "" {
		return nil
	}
	if _, err := uploadPermissionIdentityID(ctx, userID); err != nil {
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
		slog.Error(
			"SpiceDB permission check failed during part upload",
			"error", err,
			"resourceType", target.resourceType,
			"entityID", entityID,
			"userID", userID,
		)
		return fmt.Errorf("permission check failed")
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

func uploadPermissionIdentityID(ctx context.Context, _ string) (string, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || strings.TrimSpace(user.IdentityID.String()) == "" {
		return "", errs.AuthenticationRequired()
	}
	return user.IdentityID.String(), nil
}

func checkSpiceDBCan(
	ctx context.Context,
	user *auth.UserInfo,
	can policyv1.Can,
	spiceDB *auth.SpiceDBClient,
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
