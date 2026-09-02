package campaign

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
)

func TestCampaignOwnsSealedDeliveryRenderDefinition(t *testing.T) {
	snapshot := CampaignDeliverySnapshot{
		Subject:      "Subject",
		ContentHTML:  "<p>Body</p>",
		SourceLocale: "en",
		Translations: []CampaignDeliverySnapshotTranslation{{
			Locale: "en", Subject: "Subject", ContentHTML: "<p>Body</p>",
		}},
	}
	renderSnapshot, err := campaignDeliverySnapshotJSONFields(snapshot)
	require.NoError(t, err)
	encodedSnapshot, err := json.Marshal(renderSnapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encodedSnapshot), `"status"`)

	campaignID := "campaign-a"
	campaignRun := model.CampaignDeliveryRun{
		RunKind:               EmailDeliveryRunKindCampaign,
		CampaignID:            &campaignID,
		DefinitionSealed:      true,
		SnapshotSchemaVersion: CampaignDeliverySnapshotSchemaVersion,
		TemplateData:          model.JSONFields{},
		RenderSnapshot:        renderSnapshot,
	}
	require.NoError(t, ValidateEmailDeliveryRenderDefinition(campaignRun))
	require.Equal(t, "campaign:campaign-a", EmailDeliveryRunTemplateType(campaignRun))
	require.Equal(t, campaignID, EmailDeliveryRunReferenceID(campaignRun))
	templateData, err := CampaignDeliveryRunTemplateData(campaignRun)
	require.NoError(t, err)
	require.Empty(t, templateData)

	termsID := "terms-a"
	eventKey := "terms_effective"
	legalRun := model.CampaignDeliveryRun{
		RunKind:               EmailDeliveryRunKindLegalNotice,
		TermsID:               &termsID,
		DefinitionSealed:      true,
		SnapshotSchemaVersion: CampaignDeliverySnapshotSchemaVersion,
		TemplateEventKey:      &eventKey,
		TemplateData:          model.JSONFields{"terms_url": "https://example.test/terms"},
		RenderSnapshot:        renderSnapshot,
	}
	require.NoError(t, ValidateEmailDeliveryRenderDefinition(legalRun))
	require.Equal(t, eventKey, EmailDeliveryRunTemplateType(legalRun))
	require.Equal(t, termsID, EmailDeliveryRunReferenceID(legalRun))
	templateData, err = CampaignDeliveryRunTemplateData(legalRun)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"terms_url": "https://example.test/terms"}, templateData)

	campaignRun.DefinitionSealed = false
	require.Error(t, ValidateEmailDeliveryRenderDefinition(campaignRun))
}

func TestSealLegalNoticeDeliveryRunValidatesCampaignOwnedAuthorityBeforeWrite(t *testing.T) {
	snapshot := CampaignDeliverySnapshot{
		Subject:      "Subject",
		ContentHTML:  "<p>Body</p>",
		SourceLocale: "en",
		Translations: []CampaignDeliverySnapshotTranslation{{
			Locale: "en", Subject: "Subject", ContentHTML: "<p>Body</p>",
		}},
	}
	termsID := "terms-a"
	privacyID := "privacy-a"
	version := int32(1)

	err := func() error {
		_, err := SealLegalNoticeDeliveryRun(
			context.Background(),
			nil,
			LegalNoticeDeliveryRunDefinition{
				TermsID:                 &termsID,
				PrivacyID:               &privacyID,
				TemplateEventKey:        "terms_effective",
				TemplateData:            map[string]string{"terms_url": "https://example.test/terms"},
				Snapshot:                snapshot,
				SourceTemplateID:        "template-a",
				SourceTemplateUpdatedAt: time.Now().UTC(),
				SourceTermsVersion:      &version,
			},
		)
		return err
	}()
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestDecodeCampaignDeliverySnapshotRequiresClosedCompleteTranslations(t *testing.T) {
	valid := []byte(`{
		"subject":"",
		"content_html":"",
		"source_locale":"ko",
		"translations":[{
			"locale":"ko",
			"subject":"",
			"content_html":""
		}],
		"layout_source_locale":"en",
		"layout_translations":[{
			"locale":"en",
			"html_content":""
		}]
	}`)
	_, err := decodeCampaignDeliverySnapshotJSON(valid)
	require.NoError(t, err)

	for name, raw := range map[string]string{
		"missing content html": `{
			"subject":"","source_locale":"ko","translations":[]
		}`,
		"missing translation subject": `{
			"subject":"","content_html":"","source_locale":"ko",
			"translations":[{"locale":"ko","content_html":""}]
		}`,
		"mismatched source locale": `{
			"subject":"","content_html":"","source_locale":"ko",
			"translations":[{"locale":"en","subject":"","content_html":""}]
		}`,
		"unknown translation key": `{
			"subject":"","content_html":"","source_locale":"ko",
			"translations":[{
				"locale":"ko","subject":"","content_html":"",
				"template_id":"forbidden"
			}]
		}`,
		"layout half": `{
			"subject":"","content_html":"","source_locale":"ko",
			"translations":[{"locale":"ko","subject":"","content_html":""}],
			"layout_source_locale":"ko"
		}`,
		"legacy translation status": `{
			"subject":"","content_html":"","source_locale":"ko",
			"translations":[{"locale":"ko","status":"published","subject":"","content_html":""}]
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeCampaignDeliverySnapshotJSON([]byte(raw))
			require.Error(t, err)
		})
	}
}

func TestDecodeCampaignDeliveryTemplateDataRejectsUnknownAndNonStringValues(t *testing.T) {
	data, err := decodeCampaignDeliveryTemplateData(
		EmailDeliveryRunKindLegalNotice,
		"terms_update",
		model.JSONFields{
			"policy_title":   "Contributor terms",
			"effective_date": "2026-07-30",
			"preview_url":    "https://example.test/terms/preview",
		},
	)
	require.NoError(t, err)
	require.Equal(t, "2026-07-30", *data.EffectiveDate)

	_, err = decodeCampaignDeliveryTemplateData(
		EmailDeliveryRunKindCampaign,
		"",
		model.JSONFields{"recipient_email": "must-only-exist-in-worker-memory"},
	)
	require.ErrorContains(t, err, "unknown key")

	_, err = decodeCampaignDeliveryTemplateData(
		EmailDeliveryRunKindCampaign,
		"",
		model.JSONFields{"unexpected": 7},
	)
	require.ErrorContains(t, err, "must be a string")
}
