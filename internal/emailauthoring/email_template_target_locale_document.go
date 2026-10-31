package emailauthoring

import (
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

func emailTemplateSourceMetadataProjection(state emailcontent.ExactLocaleState) *intrav1.EmailTemplateLocaleMetadata {
	subject := state.SourceMetadata.Subject.String
	return &intrav1.EmailTemplateLocaleMetadata{Locale: state.SourceLocale, Subject: &subject}
}

func emailTemplateLocaleMetadataProjection(
	state emailcontent.ExactLocaleState,
	locale string,
) *intrav1.EmailTemplateLocaleMetadata {
	if state.TargetMetadata == nil {
		return nil
	}
	subject := state.SourceMetadata.Subject.String
	if state.TargetMetadata.Subject.Valid {
		subject = state.TargetMetadata.Subject.String
	}
	return &intrav1.EmailTemplateLocaleMetadata{Locale: locale, Subject: &subject}
}
