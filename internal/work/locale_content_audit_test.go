package work

import (
	"context"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

type workLocaleContentAuditCapture struct {
	records []sharedtelemetry.AuditRecord
}

func (capture *workLocaleContentAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.records = append(capture.records, record)
	return nil
}
