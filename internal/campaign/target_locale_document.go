package campaign

import (
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func campaignLocaleMetadataProjection(
	state emailcontent.ExactLocaleState,
	locale string,
) *intrav1.CampaignLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := state.SourceMetadata.Subject.String
	if state.TargetMetadata.Subject.Valid {
		subject = state.TargetMetadata.Subject.String
	}
	return &intrav1.CampaignLocaleMetadata{Locale: locale, Subject: &subject}
}

func campaignSourceMetadataProjection(state emailcontent.ExactLocaleState) *intrav1.CampaignLocaleMetadata {
	subject := state.SourceMetadata.Subject.String
	return &intrav1.CampaignLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}
