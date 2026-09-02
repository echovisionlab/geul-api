package sitesettings

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func validateSiteSettingKeys(settings []*managev1.SiteSetting) error {
	invalidKeys := make([]string, 0)
	for _, setting := range settings {
		if _, ok := defaultSettings[setting.Key]; !ok {
			invalidKeys = append(invalidKeys, setting.Key)
		}
	}
	if len(invalidKeys) == 0 {
		return nil
	}
	return errs.InvalidArgument("keys", fmt.Sprintf("invalid setting keys: %v", invalidKeys))
}

func (s *SiteSettingService) applySiteSettingBatch(
	ctx context.Context,
	requested []*managev1.SiteSetting,
	can policyv1.Can,
) (*string, error) {
	var ogGenerationRunID *string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settings, err := s.loadSettingsRowForUpdate(tx)
		if err != nil {
			return err
		}
		before := cloneSiteSettingsForComparison(settings)
		if err := s.applyRequestedSiteSettings(settings, requested); err != nil {
			return err
		}
		requestedKeys := make([]string, 0, len(requested))
		for _, setting := range requested {
			requestedKeys = append(requestedKeys, setting.Key)
		}
		changedKeys := s.changedSiteSettingKeys(&before, settings, requestedKeys)
		if len(changedKeys) == 0 {
			return nil
		}
		if err := s.validateSiteSettingBatchReferences(ctx, tx, settings, requested); err != nil {
			return err
		}
		if err := s.lockAndValidateSiteSettingAssets(ctx, tx, settings, requested); err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		settings.UpdatedAt = time.Now()
		if err := tx.Save(settings).Error; err != nil {
			return err
		}
		if err := s.syncSiteSettingBatchBindings(ctx, tx, settings, changedKeys); err != nil {
			return err
		}
		runID, err := s.og.Request(
			ctx, tx, &before, settings, changedKeys,
		)
		if err != nil {
			return err
		}
		ogGenerationRunID = runID
		return s.appendSettingsAudit(ctx, tx, changedKeys)
	})
	return ogGenerationRunID, err
}

func cloneSiteSettingsForComparison(settings *model.SiteSettings) model.SiteSettings {
	clone := *settings
	clone.OGImageConfig = append([]byte(nil), settings.OGImageConfig...)
	return clone
}

func (s *SiteSettingService) applyRequestedSiteSettings(
	settings *model.SiteSettings,
	requested []*managev1.SiteSetting,
) error {
	for _, setting := range requested {
		var value structured.Value
		if setting.Value != nil {
			value = setting.Value.AsInterface()
		}
		if err := s.applySettingValue(settings, setting.Key, value); err != nil {
			return errs.InvalidArgument(
				"value",
				fmt.Sprintf("invalid value for key %s: %v", setting.Key, err),
			)
		}
	}
	return nil
}

func (s *SiteSettingService) validateSiteSettingBatchReferences(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	requested []*managev1.SiteSetting,
) error {
	for _, setting := range requested {
		if setting.Key == "homepage_page_id" || isSiteSettingMenuReference(setting.Key) {
			if err := s.references.Validate(ctx, tx, setting.Key, siteSettingReferenceValue(settings, setting.Key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SiteSettingService) lockAndValidateSiteSettingAssets(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	requested []*managev1.SiteSetting,
) error {
	fileIDs := make([]string, 0, len(requested))
	for _, setting := range requested {
		fileID, _, _, managed := siteSettingAssetBinding(settings, setting.Key)
		if managed && fileID != nil {
			fileIDs = append(fileIDs, *fileID)
		}
	}
	if err := s.assets.LockForAttachment(ctx, tx, fileIDs); err != nil {
		return err
	}
	return s.validateUniqueSiteSettingAssets(ctx, tx, settings, requested)
}

func (s *SiteSettingService) validateUniqueSiteSettingAssets(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	requested []*managev1.SiteSetting,
) error {
	validatedKeys := make(map[string]struct{}, len(requested))
	for _, setting := range requested {
		if _, exists := validatedKeys[setting.Key]; exists {
			continue
		}
		validatedKeys[setting.Key] = struct{}{}
		fileID, _, _, managed := siteSettingAssetBinding(settings, setting.Key)
		if managed && fileID != nil {
			if err := s.assets.ValidateAttachment(ctx, tx, setting.Key, *fileID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SiteSettingService) syncSiteSettingBatchBindings(
	ctx context.Context,
	tx *gorm.DB,
	settings *model.SiteSettings,
	changedKeys []string,
) error {
	for _, key := range changedKeys {
		if err := s.syncSiteSettingAssetBinding(ctx, tx, settings, key); err != nil {
			return err
		}
	}
	return nil
}
