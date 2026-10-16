package sitevariablemigrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/menu"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type tableSpec struct {
	table, column, owner, locale string
	jsonKind                     string
}

var tables = []tableSpec{
	{"content_block", "shared_data", "document_id", "", "rich"},
	{"content_block_locale", "localized_data", "block_id", "locale", "rich"},
	{"email_layout_translation", "html_content", "entity_id", "locale", "email"},
	{"email_template_translation", "subject", "entity_id", "locale", "text"},
	{"email_template_translation", "content_html", "entity_id", "locale", "email"},
	{"menu", "items", "id", "", "menu"},
	{"menu_translation", "items_json", "entity_id", "locale", "menu"},
	{"page_translation", "summary", "entity_id", "locale", "text"},
}

type change struct {
	spec          tableSpec
	key           json.RawMessage
	before        json.RawMessage
	after         string
	locale, owner string
}

// Run never opens a database itself. The command requires an explicit DATABASE_URL.
func Run(ctx context.Context, db *gorm.DB, apply bool, report io.Writer) error {
	return run(ctx, db, apply, report, false)
}

// RunContactEmail rewrites only live legacy contact atoms and reserved tokens.
// It shares Run's owner locks, before-image backups, validation and revisions.
// Callers must stop content writers and close collaboration rooms before apply;
// this maintenance path does not emit live content invalidation events.
func RunContactEmail(ctx context.Context, db *gorm.DB, apply bool, report io.Writer) error {
	return run(ctx, db, apply, report, true)
}

func run(ctx context.Context, db *gorm.DB, apply bool, report io.Writer, contactOnly bool) error {
	owners := map[string][]change{}
	counts := map[string]int{}
	layoutOwners := map[string]layoutMigrationOwner{}
	for _, spec := range tables {
		var exists bool
		if err := db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?)`, spec.table, spec.column).Scan(&exists).Error; err != nil {
			return err
		}
		if !exists {
			continue
		}
		// Primary-key JSON avoids PostgreSQL physical row identifiers and works for
		// compound locale keys. Identifiers below come only from the static allowlist.
		var rows []struct{ Row []byte }
		if err := db.WithContext(ctx).Raw(`SELECT to_jsonb(t) AS row FROM "` + spec.table + `" t ORDER BY ` + spec.owner + func() string {
			if spec.locale != "" {
				return ", " + spec.locale
			}
			return ""
		}()).Scan(&rows).Error; err != nil {
			return err
		}
		if spec.table == "email_layout_translation" {
			var err error
			layoutOwners, err = planLayoutChanges(ctx, db, rows, contactOnly)
			if err != nil {
				return err
			}
		}
		for _, row := range rows {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(row.Row, &fields); err != nil {
				return err
			}
			raw := fields[spec.column]
			if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
				continue
			}
			var next []byte
			var err error
			switch spec.jsonKind {
			case "rich":
				if contactOnly {
					next, err = ContactRichJSON(raw)
				} else {
					next, err = RichJSON(raw)
				}
			case "menu":
				if contactOnly {
					next, err = ContactMenuJSON(raw)
				} else {
					next, err = MenuJSON(raw)
				}
			default:
				var source string
				err = json.Unmarshal(raw, &source)
				if err == nil {
					if spec.jsonKind == "email" {
						if spec.table == "email_layout_translation" {
							var id, locale string
							_ = json.Unmarshal(fields["entity_id"], &id)
							_ = json.Unmarshal(fields["locale"], &locale)
							source = layoutOwners[id].Locales[locale]
						} else if contactOnly {
							source = ContactText(source)
						} else {
							source = EmailSource(source)
						}
					} else if contactOnly {
						source = ContactText(source)
					} else {
						source = Text(source)
					}
					next, err = json.Marshal(source)
				}
			}
			if err != nil {
				return fmt.Errorf("%s.%s: %w", spec.table, spec.column, err)
			}
			var oldValue, newValue any
			_ = json.Unmarshal(raw, &oldValue)
			_ = json.Unmarshal(next, &newValue)
			canonicalOld, _ := json.Marshal(oldValue)
			canonicalNew, _ := json.Marshal(newValue)
			if bytes.Equal(canonicalOld, canonicalNew) {
				continue
			}
			if spec.jsonKind == "rich" {
				var blockID string
				identity := "id"
				if spec.table == "content_block_locale" {
					identity = "block_id"
				}
				if err := json.Unmarshal(fields[identity], &blockID); err != nil {
					return err
				}
				var authority struct{ Profile, Kind string }
				if err := db.WithContext(ctx).Table("content_block AS b").Select("d.profile, b.kind").Joins("JOIN content_document d ON d.id = b.document_id").Where("b.id = ?", blockID).Take(&authority).Error; err != nil {
					return err
				}
				mode := contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT
				if spec.table == "content_block" {
					_, err = contentv1.NormalizeContentStorageShared(authority.Profile, authority.Kind, next, mode)
				} else {
					_, err = contentv1.NormalizeContentStorageLocale(authority.Profile, authority.Kind, next, mode)
				}
				if err != nil {
					return fmt.Errorf("validate migration Block %s: %w", blockID, err)
				}
			}
			var owner, locale string
			_ = json.Unmarshal(fields[spec.owner], &owner)
			if spec.locale != "" {
				_ = json.Unmarshal(fields[spec.locale], &locale)
			}
			key := map[string]json.RawMessage{spec.owner: fields[spec.owner]}
			if spec.table == "content_block" {
				key = map[string]json.RawMessage{"id": fields["id"]}
			}
			if spec.locale != "" {
				key[spec.locale] = fields[spec.locale]
			}
			ownerGroup := spec.table + ":" + owner
			switch spec.table {
			case "menu_translation":
				ownerGroup = "menu:" + owner
			case "email_layout_translation":
				root := layoutOwners[owner]
				ownerGroup = "email_layout:" + owner
				if root.ContentDocumentID != nil && *root.ContentDocumentID != "" {
					ownerGroup = "document:" + *root.ContentDocumentID
				}
			case "email_template_translation", "page_translation":
				rootTable := "email_template"
				if spec.table == "page_translation" {
					rootTable = "page"
				}
				var root struct{ ContentDocumentID *string }
				if err := db.Table(rootTable).Select("content_document_id").Where("id = ?", owner).Scan(&root).Error; err != nil {
					return err
				}
				if root.ContentDocumentID != nil && *root.ContentDocumentID != "" {
					ownerGroup = "document:" + *root.ContentDocumentID
				}
			}
			if spec.table == "content_block_locale" {
				var documentID string
				if err := db.WithContext(ctx).Table("content_block").Select("document_id").Where("id = ?", owner).Scan(&documentID).Error; err != nil {
					return err
				}
				ownerGroup = "document:" + documentID
			} else if spec.table == "content_block" {
				ownerGroup = "document:" + owner
			}
			keyJSON, _ := json.Marshal(key)
			after := string(next)
			if spec.jsonKind == "text" || spec.jsonKind == "email" {
				_ = json.Unmarshal(next, &after)
			}
			owners[ownerGroup] = append(owners[ownerGroup], change{spec, keyJSON, row.Row, after, locale, owner})
			counts[spec.table+"."+spec.column+" locale="+locale]++
		}
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(report, "%s changes=%d\n", key, counts[key]); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(report, "owners=%d apply=%t\n", len(owners), apply); err != nil {
		return err
	}
	if !apply || len(owners) == 0 {
		return nil
	}
	runID := uuid.NewString()
	if err := ensureBackupTable(ctx, db); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(report, "run_id=%s\n", runID); err != nil {
		return err
	}
	ownerKeys := make([]string, 0, len(owners))
	for key := range owners {
		ownerKeys = append(ownerKeys, key)
	}
	sort.Slice(ownerKeys, func(i, j int) bool {
		hasLayout := func(owner string) bool {
			for _, c := range owners[owner] {
				if c.spec.table == "email_layout_translation" {
					return true
				}
			}
			return false
		}
		if hasLayout(ownerKeys[i]) != hasLayout(ownerKeys[j]) {
			return hasLayout(ownerKeys[i])
		}
		return ownerKeys[i] < ownerKeys[j]
	})
	for _, owner := range ownerKeys {
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			documentID := ""
			if len(owner) > 9 && owner[:9] == "document:" {
				documentID = owner[9:]
			}
			rewrite := func() error {
				if documentID != "" {
					if err := tx.Exec(`SELECT id FROM content_document WHERE id = ? FOR UPDATE`, documentID).Error; err != nil {
						return err
					}
				}
				for _, c := range owners[owner] {
					// Capture the current row under lock. Several changed columns share
					// its first before image; column predicates below reject stale plans.
					var locked struct{ Row []byte }
					if err := tx.Raw(`SELECT to_jsonb(t) AS row FROM "`+c.spec.table+`" t WHERE to_jsonb(t) @> ?::jsonb FOR UPDATE`, string(c.key)).Scan(&locked).Error; err != nil {
						return err
					}
					if len(locked.Row) == 0 {
						return fmt.Errorf("migration owner row disappeared: %s", c.spec.table)
					}
					if err := tx.Exec(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) VALUES(?,?,?,?::jsonb,?::jsonb) ON CONFLICT(run_id,table_name,row_key) DO NOTHING`, runID, owner, c.spec.table, string(c.key), string(locked.Row)).Error; err != nil {
						return err
					}
					var fields map[string]json.RawMessage
					_ = json.Unmarshal(c.key, &fields)
					query := tx.Table(c.spec.table)
					for field, value := range fields {
						var scalar string
						_ = json.Unmarshal(value, &scalar)
						query = query.Where(`"`+field+`" = ?`, scalar)
					}
					var original map[string]json.RawMessage
					_ = json.Unmarshal(c.before, &original)
					if c.spec.jsonKind == "rich" || c.spec.jsonKind == "menu" {
						query = query.Where(`"`+c.spec.column+`" = ?::jsonb`, string(original[c.spec.column]))
					} else {
						var old string
						_ = json.Unmarshal(original[c.spec.column], &old)
						query = query.Where(`"`+c.spec.column+`" = ?`, old)
					}
					var result *gorm.DB
					if c.spec.jsonKind == "rich" || c.spec.jsonKind == "menu" {
						result = query.UpdateColumn(c.spec.column, gorm.Expr("?::jsonb", c.after))
					} else {
						result = query.UpdateColumn(c.spec.column, c.after)
					}
					if result.Error != nil {
						return result.Error
					}
					if result.RowsAffected != 1 {
						return fmt.Errorf("migration conflict: %s %s", c.spec.table, c.key)
					}
				}
				validationChanges := append([]change(nil), owners[owner]...)
				for id, layout := range layoutOwners {
					touched := false
					for _, c := range owners[owner] {
						if c.spec.table == "email_layout_translation" && c.owner == id {
							touched = true
							break
						}
					}
					if !touched {
						continue
					}
					locales := make([]string, 0, len(layout.Locales))
					for locale := range layout.Locales {
						locales = append(locales, locale)
					}
					sort.Strings(locales)
					for _, locale := range locales {
						key, _ := json.Marshal(map[string]string{"entity_id": id, "locale": locale})
						validationChanges = append(validationChanges, change{spec: tableSpec{table: "email_layout_translation"}, owner: id, locale: locale, key: key})
					}
				}
				if err := validateMigratedEmails(ctx, tx, validationChanges); err != nil {
					return err
				}
				if documentID != "" {
					if err := tx.Exec(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) SELECT ?, ?, 'content_document', jsonb_build_object('id',t.id), to_jsonb(t) FROM content_document t WHERE id = ? ON CONFLICT(run_id,table_name,row_key) DO NOTHING`, runID, owner, documentID).Error; err != nil {
						return err
					}
					if err := tx.Table("content_document").Where("id = ?", documentID).UpdateColumn("revision", uuid.New()).Error; err != nil {
						return err
					}
					if err := legal.RegenerateSiteVariableMigrationProjections(ctx, tx, documentID, runID); err != nil {
						return err
					}
				}
				return nil
			}
			if menuID, ok := strings.CutPrefix(owner, "menu:"); ok {
				return menu.MigrateSiteVariableURLs(ctx, tx, menuID, func(id uuid.UUID) error {
					// The callback runs after the Menu owner locks its root and document.
					for _, backup := range []struct{ table, id string }{{"menu", menuID}, {"content_document", id.String()}} {
						if err := tx.Exec(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) SELECT ?, ?, ?, jsonb_build_object('id',t.id), to_jsonb(t) FROM `+backup.table+` t WHERE id = ? ON CONFLICT(run_id,table_name,row_key) DO NOTHING`, runID, owner, backup.table, backup.id).Error; err != nil {
							return err
						}
					}
					return rewrite()
				})
			}
			lockedRewrite := rewrite
			layoutIDs := []string{}
			for _, c := range owners[owner] {
				if c.spec.table == "email_layout_translation" && !slices.Contains(layoutIDs, c.owner) {
					layoutIDs = append(layoutIDs, c.owner)
				}
			}
			sort.Strings(layoutIDs)
			for i := len(layoutIDs) - 1; i >= 0; i-- {
				next, layoutID := lockedRewrite, layoutIDs[i]
				lockedRewrite = func() error {
					return email.MigrateLayoutMarkers(ctx, tx, layoutID, layoutOwners[layoutID].SourceLocale, layoutOwners[layoutID].Before, func() error {
						if err := tx.Exec(`INSERT INTO site_variable_migration_backup(run_id,owner_key,table_name,row_key,before_image) SELECT ?, ?, 'email_layout', jsonb_build_object('id',t.id), to_jsonb(t) FROM email_layout t WHERE id = ? ON CONFLICT(run_id,table_name,row_key) DO NOTHING`, runID, owner, layoutID).Error; err != nil {
							return err
						}
						return next()
					})
				}
			}
			return lockedRewrite()
		}); err != nil {
			var invalid *emailValidationError
			if errors.As(err, &invalid) {
				if _, reportErr := fmt.Fprintf(report, "skipped owner=%s changes=%d: %s\n", owner, len(owners[owner]), err); reportErr != nil {
					return reportErr
				}
				continue
			}
			return fmt.Errorf("owner %s: %w", owner, err)
		}
	}
	return nil
}
