package page

import (
	"context"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

type pageLocaleContentAuditCapture struct {
	records []sharedtelemetry.AuditRecord
}

func (capture *pageLocaleContentAuditCapture) AppendDomainAuditInTransaction(
	_ context.Context,
	_ *gorm.DB,
	record sharedtelemetry.AuditRecord,
) error {
	capture.records = append(capture.records, record)
	return nil
}
