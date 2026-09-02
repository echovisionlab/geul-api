package application

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type sourceLocaleAuditCall struct {
	entityType     string
	entityID       string
	previousLocale string
	newLocale      string
}

type recordingSourceLocaleAuditDomains struct {
	testTranslationDomains
	calls []sourceLocaleAuditCall
}

func (domains *recordingSourceLocaleAuditDomains) AppendSourceLocaleAudit(
	_ context.Context,
	_ *gorm.DB,
	entityType string,
	entityID string,
	previousLocale string,
	newLocale string,
) error {
	domains.calls = append(domains.calls, sourceLocaleAuditCall{
		entityType: entityType, entityID: entityID,
		previousLocale: previousLocale, newLocale: newLocale,
	})
	return nil
}

func TestSourceLocaleAuditDelegatesToOwningDomain(t *testing.T) {
	domains := &recordingSourceLocaleAuditDomains{}
	service := &TranslationService{domains: domains}

	err := service.appendSourceLocaleSwitchAudit(t.Context(), nil, &sourceLocaleSwitchState{
		entityType: "post", entityID: "post-1", previousLocale: "en", requestedLocale: "ko",
	})

	require.NoError(t, err)
	require.Equal(t, []sourceLocaleAuditCall{{
		entityType: "post", entityID: "post-1", previousLocale: "en", newLocale: "ko",
	}}, domains.calls)
}
