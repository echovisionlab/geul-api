package translationadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/og"
	core "github.com/echovisionlab/geul-api/internal/translation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sourceLocaleAuditBuilder func(
	sharedtelemetry.AuditMetadata,
	string,
	string,
	string,
) (sharedtelemetry.AuditRecord, error)

func sourceLocaleAudit(
	auditWriter domainaudit.Appender,
	action sharedtelemetry.AuditAction,
	builder sourceLocaleAuditBuilder,
) func(context.Context, *gorm.DB, string, string, string) error {
	if builder == nil {
		panic("translation source-locale Audit builder is required")
	}
	return func(ctx context.Context, tx *gorm.DB, entityID string, previousLocale string, newLocale string) error {
		if auditWriter == nil {
			return errs.InternalMsg("translation source-locale Audit writer is required")
		}
		return domainaudit.AppendRequest(ctx, tx, auditWriter, action, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return builder(metadata, entityID, previousLocale, newLocale)
		})
	}
}

type legalSourceLocaleAuditBuilder func(
	sharedtelemetry.AuditMetadata,
	string,
	int64,
	string,
	string,
) (sharedtelemetry.AuditRecord, error)

func legalSourceLocaleAudit(
	auditWriter domainaudit.Appender,
	table string,
	entityType string,
	builder legalSourceLocaleAuditBuilder,
) func(context.Context, *gorm.DB, string, string, string) error {
	if table == "" || entityType == "" || builder == nil {
		panic("legal source-locale Audit dependencies are required")
	}
	return func(ctx context.Context, tx *gorm.DB, entityID string, previousLocale string, newLocale string) error {
		if auditWriter == nil {
			return errs.InternalMsg("translation source-locale Audit writer is required")
		}
		var row struct {
			Version int64 `gorm:"column:version"`
		}
		if err := tx.WithContext(ctx).Table(table).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("version").Where("id = ?", entityID).Take(&row).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound(entityType, entityID)
			}
			return errs.Internal(err)
		}
		return domainaudit.AppendRequest(ctx, tx, auditWriter, sharedtelemetry.AuditLegalPolicyUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return builder(metadata, entityID, row.Version, previousLocale, newLocale)
		})
	}
}

func prepareScalarSourceLocale(domain core.Kind) func(context.Context, *gorm.DB, string, string, string, time.Time) error {
	definition, ok := core.DefinitionForKind(string(domain))
	if !ok {
		panic(fmt.Sprintf("translation domain %q definition is missing", domain))
	}
	return func(ctx context.Context, db *gorm.DB, entityID string, _ string, requestedLocale string, now time.Time) error {
		result := db.WithContext(ctx).Exec(
			"INSERT INTO "+definition.EntryTable+" (entity_id, locale, created_at, updated_at) "+
				"VALUES (?, ?, ?, ?) ON CONFLICT (entity_id, locale) DO NOTHING",
			entityID,
			requestedLocale,
			now.UTC(),
			now.UTC(),
		)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		return nil
	}
}

func localeAwareOG(domain core.Kind) func(context.Context, *gorm.DB, *og.Planner, *og.Refresher, string, string, string) (bool, error) {
	entityType := string(domain)
	if !og.SupportsLocaleAware(entityType) {
		panic(fmt.Sprintf("translation domain %q does not support locale-aware OG", domain))
	}
	return func(ctx context.Context, tx *gorm.DB, _ *og.Planner, refresher *og.Refresher, entityID string, locale string, reason string) (bool, error) {
		_, err := refresher.RequestCurrentWithDB(
			ctx, tx, og.EntityTypeForName(entityType), entityID, locale, false, reason,
		)
		return true, err
	}
}

func requireJobEdit(factory func(string) (policyv1.Can, error)) func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	if factory == nil {
		panic("translation job permission factory is required")
	}
	return func(ctx context.Context, _ *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
		can, err := factory(entityID)
		if err != nil {
			return errs.InvalidArgument("filters.entity_id", err.Error())
		}
		decision, err := auth.AuthorizationDecision(ctx, can)
		if err != nil {
			return errs.AuthenticationRequired()
		}
		allowed, err := spiceDB.Can(ctx, decision)
		if err != nil {
			return errs.Internal(fmt.Errorf("check translation entity permission: %w", err))
		}
		if !allowed {
			return errs.NoPermission("view translation jobs for", can.Resource().Type())
		}
		return nil
	}
}

func genericInterchangeView(
	domain core.Kind,
	permissions translationCanSet,
	archivedStatus string,
) func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	definition, ok := core.DefinitionForKind(string(domain))
	if !ok {
		panic(fmt.Sprintf("translation domain %q definition is missing", domain))
	}
	return func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
		var root struct {
			ID     string `gorm:"column:id"`
			Status string `gorm:"column:status"`
		}
		columns := "id"
		if archivedStatus != "" {
			columns = "id, status"
		}
		if err := tx.WithContext(ctx).Table(definition.RootTable).
			Clauses(clause.Locking{Strength: "SHARE"}).
			Select(columns).Where("id = ?", entityID).Take(&root).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound(string(domain), entityID)
			}
			return errs.Internal(err)
		}
		if err := requireActiveTranslationPrincipal(ctx, tx, string(domain), entityID); err != nil {
			return err
		}
		action := permissions.view
		if archivedStatus != "" && root.Status == archivedStatus {
			action = permissions.viewArchived
		}
		return requireTranslationPermission(ctx, spiceDB, entityID, action)
	}
}

func genericSourceLocaleEdit(
	domain core.Kind,
	permissions translationCanSet,
	archivedStatus string,
) func(context.Context, *gorm.DB, *auth.SpiceDBClient, string) error {
	definition, ok := core.DefinitionForKind(string(domain))
	if !ok {
		panic(fmt.Sprintf("translation domain %q definition is missing", domain))
	}
	return func(ctx context.Context, tx *gorm.DB, spiceDB *auth.SpiceDBClient, entityID string) error {
		if err := requireActiveTranslationPrincipal(ctx, tx, string(domain), entityID); err != nil {
			return err
		}
		action := permissions.edit
		if archivedStatus != "" {
			var root struct {
				Status string `gorm:"column:status"`
			}
			if err := tx.WithContext(ctx).Table(definition.RootTable).Select("status").
				Where("id = ?::uuid", entityID).Take(&root).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return errs.NotFound(string(domain), entityID)
				}
				return errs.Internal(err)
			}
			if root.Status == archivedStatus {
				action = permissions.editArchived
			}
		}
		return requireTranslationPermission(ctx, spiceDB, entityID, action)
	}
}

func requireActiveTranslationPrincipal(ctx context.Context, tx *gorm.DB, entityType string, entityID string) error {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return errs.AuthenticationRequired()
	}
	active, err := identitystate.LockActivePrincipal(ctx, tx, principal)
	if err != nil {
		return errs.Internal(err)
	}
	if !active {
		return errs.NotFound(entityType, entityID)
	}
	return nil
}

func requireTranslationPermission(
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	entityID string,
	action auth.ResourceAction,
) error {
	if action == nil {
		return errs.InternalMsg("translation domain action is not configured")
	}
	can, err := action(entityID)
	if err != nil {
		return err
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := spiceDB.Can(ctx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NotFound(can.Resource().Type(), entityID)
	}
	return nil
}
