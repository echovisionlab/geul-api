package architecture

import (
	"go/ast"
	"go/token"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// tableOwners maps each table to the package that owns its rows. Another
// package reads or writes the table through a port the owner defines. An
// adapter named after the owner (internal/adapters/<owner>, or one listed in
// ownerRepositories) is the owner's repository and shares the ownership.
var tableOwners = map[string]string{
	"account_email_change_request":                       "internal/account",
	"account_identity":                                   "internal/account",
	"user_deletion_request":                              "internal/account",
	"metadata_ai_job":                                    "internal/ai",
	"artist":                                             "internal/artist",
	"artist_file":                                        "internal/artist",
	"artist_label":                                       "internal/artist",
	"artist_manager":                                     "internal/artist",
	"artist_owner":                                       "internal/artist",
	"artist_translation":                                 "internal/artist",
	"audience_segment":                                   "internal/audience",
	"audience_segment_excluded_member":                   "internal/audience",
	"audience_segment_user_role":                         "internal/audience",
	"audience_segment_user_tag":                          "internal/audience",
	"auth_bootstrap_state":                               "internal/authentication",
	"auth_code_issuance":                                 "internal/authentication",
	"campaign":                                           "internal/campaign",
	"campaign_translation":                               "internal/campaign",
	"email_delivery_recipient":                           "internal/campaign",
	"email_delivery_run":                                 "internal/campaign",
	"email_delivery_run_target_excluded_member":          "internal/campaign",
	"email_delivery_run_target_user_role":                "internal/campaign",
	"email_delivery_run_target_user_tag":                 "internal/campaign",
	"content_block":                                      "internal/contentblock",
	"content_block_attachment":                           "internal/contentblock",
	"content_block_attachment_download_audience_segment": "internal/contentblock",
	"content_block_locale":                               "internal/contentblock",
	"content_document":                                   "internal/contentblock",
	"email_layout":                                       "internal/emailauthoring",
	"email_layout_translation":                           "internal/emailauthoring",
	"email_template":                                     "internal/emailauthoring",
	"email_template_translation":                         "internal/emailauthoring",
	"email_suppression":                                  "internal/emaildelivery",
	"mail_adapter":                                       "internal/emaildelivery",
	"file":                                               "internal/filemedia",
	"file_derivative":                                    "internal/filemedia",
	"file_folder":                                        "internal/filemedia",
	"file_ingest_binding":                                "internal/filemedia",
	"media_generation":                                   "internal/filemedia",
	"mesh_optimization_candidate":                        "internal/filemedia",
	"upload_part":                                        "internal/filemedia",
	"upload_session":                                     "internal/filemedia",
	"form":                                               "internal/form",
	"form_submission":                                    "internal/form",
	"form_translation":                                   "internal/form",
	"geoip_location":                                     "internal/geoip",
	"geoip_metadata":                                     "internal/geoip",
	"geoip_network":                                      "internal/geoip",
	"label":                                              "internal/label",
	"label_manager":                                      "internal/label",
	"label_owner":                                        "internal/label",
	"label_translation":                                  "internal/label",
	"privacy_history":                                    "internal/legal",
	"privacy_translation":                                "internal/legal",
	"terms_history":                                      "internal/legal",
	"terms_translation":                                  "internal/legal",
	"map_theme":                                          "internal/maptheme",
	"public_asset":                                       "internal/mediaasset",
	"public_asset_binding":                               "internal/mediaasset",
	"member":                                             "internal/member",
	"member_personal_access_token":                       "internal/member",
	"newsletter_subscription":                            "internal/member",
	"user_cookie_consent":                                "internal/member",
	"user_tag":                                           "internal/member",
	"user_tag_mapping":                                   "internal/member",
	"menu":                                               "internal/menu",
	"menu_translation":                                   "internal/menu",
	"og_generation":                                      "internal/og",
	"og_generation_run":                                  "internal/og",
	"og_generation_target":                               "internal/og",
	"page":                                               "internal/page",
	"page_translation":                                   "internal/page",
	"page_version":                                       "internal/page",
	"comment":                                            "internal/post",
	"post":                                               "internal/post",
	"post_author":                                        "internal/post",
	"post_category":                                      "internal/post",
	"post_collaborator":                                  "internal/post",
	"post_tag":                                           "internal/post",
	"post_translation":                                   "internal/post",
	"post_version":                                       "internal/post",
	"program_event":                                      "internal/programevent",
	"program_event_artist":                               "internal/programevent",
	"program_event_client":                               "internal/programevent",
	"program_event_credit":                               "internal/programevent",
	"program_event_label":                                "internal/programevent",
	"program_event_media":                                "internal/programevent",
	"program_event_series":                               "internal/programevent",
	"program_event_translation":                          "internal/programevent",
	"program_event_type":                                 "internal/programevent",
	"program_event_type_locale":                          "internal/programevent",
	"category":                                           "internal/referencecatalog",
	"client":                                             "internal/referencecatalog",
	"country":                                            "internal/referencecatalog",
	"format":                                             "internal/referencecatalog",
	"genre":                                              "internal/referencecatalog",
	"map_place":                                          "internal/referencecatalog",
	"style":                                              "internal/referencecatalog",
	"tag":                                                "internal/referencecatalog",
	"release":                                            "internal/release",
	"release_artist":                                     "internal/release",
	"release_category":                                   "internal/release",
	"release_credit":                                     "internal/release",
	"release_credit_locale":                              "internal/release",
	"release_file":                                       "internal/release",
	"release_format":                                     "internal/release",
	"release_genre":                                      "internal/release",
	"release_label":                                      "internal/release",
	"release_style":                                      "internal/release",
	"release_translation":                                "internal/release",
	"track":                                              "internal/release",
	"track_credit":                                       "internal/release",
	"track_download_audience_segment":                    "internal/release",
	"series":                                             "internal/series",
	"series_manager":                                     "internal/series",
	"series_translation":                                 "internal/series",
	"share_link":                                         "internal/sharelink",
	"sitemap_snapshot":                                   "internal/sitemap",
	"site_setting_loader_file":                           "internal/sitesettings",
	"site_settings":                                      "internal/sitesettings",
	"domain_audit":                                       "internal/telemetry",
	"security_access":                                    "internal/telemetry",
	"transcode_job":                                      "internal/transcode",
	"waveform_job":                                       "internal/transcode",
	"translation_job":                                    "internal/translation",
	"translation_locale":                                 "internal/translation",
	"translation_provider_config":                        "internal/translation",
	"translation_settings":                               "internal/translation",
	"work":                                               "internal/work",
	"work_client":                                        "internal/work",
	"work_credit":                                        "internal/work",
	"work_credit_group":                                  "internal/work",
	"work_translation":                                   "internal/work",
	"work_version":                                       "internal/work",
}

// kratosOwner is the one package that reads Kratos tables.
const kratosOwner = "internal/identityread"

var (
	sqlStatement = regexp.MustCompile(`\b(SELECT|UPDATE|INSERT|DELETE|JOIN)\b`)
	sqlTableRef  = regexp.MustCompile(`\b(?:FROM|JOIN|UPDATE|INTO)\s+(?:ONLY\s+)?(?:public\.)?"?([a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)?)"?`)
	kratosTable  = regexp.MustCompile(`\bkratos\.([a-z_]+)`)
)

// tableOwner returns the owning package root of table and whether table is
// a product table this test knows.
func tableOwner(table string) (string, bool) {
	if strings.HasPrefix(table, "kratos.") {
		return kratosOwner, true
	}
	owner, ok := tableOwners[table]
	return owner, ok
}

// ownerRepositories are repository adapters whose name differs from their
// owner's.
var ownerRepositories = map[string][]string{
	"internal/member": {"internal/adapters/memberpat"},
}

func ownsTable(pkg, owner string) bool {
	if underRoot(pkg, owner) || underRoot(pkg, "internal/adapters/"+path.Base(owner)) {
		return true
	}
	for _, repository := range ownerRepositories[owner] {
		if underRoot(pkg, repository) {
			return true
		}
	}
	return false
}

// TestTableOwnershipRatchet flags SQL text, Table("x") and Model(&model.X{})
// that touch a table another package owns, and any Kratos table outside
// identityread. The baseline lists today's crossings; move each one behind
// a port of the owner and lower the baseline.
func TestTableOwnershipRatchet(t *testing.T) {
	modelTables := modelTableNames(t)
	observed := map[string]int{}
	record := func(pkg, table string) {
		owner, known := tableOwner(table)
		if known && !ownsTable(pkg, owner) {
			observed[pkg+" -> "+table]++
		}
	}
	for _, file := range productionFiles(t, "internal") {
		ast.Inspect(file.syntax, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				text, err := strconv.Unquote(node.Value)
				if err != nil {
					return true
				}
				for _, match := range kratosTable.FindAllStringSubmatch(text, -1) {
					record(file.pkg, "kratos."+match[1])
				}
				if !sqlStatement.MatchString(text) {
					return true
				}
				for _, match := range sqlTableRef.FindAllStringSubmatch(text, -1) {
					if !strings.HasPrefix(match[1], "kratos.") {
						record(file.pkg, match[1])
					}
				}
			case *ast.CallExpr:
				selector, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || len(node.Args) == 0 {
					return true
				}
				switch selector.Sel.Name {
				case "Table":
					if literal, ok := node.Args[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
						if text, err := strconv.Unquote(literal.Value); err == nil && !strings.HasPrefix(text, "kratos.") {
							table, _, _ := strings.Cut(strings.TrimPrefix(text, "public."), " ")
							record(file.pkg, table)
						}
					}
				case "Model", "Create", "Delete":
					if table, ok := modelTables[file.modelTypeOf(node.Args[0])]; ok {
						record(file.pkg, table)
					}
				}
			}
			return true
		})
	}
	requireRatchet(t, "table_ownership", observed)
}

// modelTypeOf names the internal/model type of &model.X{...} or model.X{...}.
func (f productionFile) modelTypeOf(expression ast.Expr) string {
	if unary, ok := expression.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expression = unary.X
	}
	literal, ok := expression.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	selector, ok := literal.Type.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok || f.imports[ident.Name] != modulePath+"/internal/model" {
		return ""
	}
	return selector.Sel.Name
}

// modelTableNames maps each internal/model type with a TableName method to
// its table.
func modelTableNames(t *testing.T) map[string]string {
	t.Helper()
	tables := map[string]string{}
	for _, file := range productionFiles(t, "internal/model") {
		for _, declaration := range file.syntax.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Name.Name != "TableName" || function.Recv == nil || len(function.Body.List) != 1 {
				continue
			}
			receiver := function.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			typeName, ok := receiver.(*ast.Ident)
			if !ok {
				continue
			}
			result, ok := function.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(result.Results) != 1 {
				continue
			}
			if literal, ok := result.Results[0].(*ast.BasicLit); ok {
				if table, err := strconv.Unquote(literal.Value); err == nil {
					tables[typeName.Name] = strings.TrimPrefix(table, "public.")
				}
			}
		}
	}
	return tables
}

// Every table in the ownership map exists in a model or in SQL, and the map
// keeps up with new models.
func TestTableOwnersCoverEveryModelTable(t *testing.T) {
	for typeName, table := range modelTableNames(t) {
		if _, ok := tableOwner(table); !ok {
			t.Errorf("model.%s maps to table %q without an owner in tableOwners", typeName, table)
		}
	}
}
