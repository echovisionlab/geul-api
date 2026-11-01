package architecture

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const policyDescriptorsImport = contractsImport + "api/policy/v1"

// A domain's use cases depend on ports and its own types only
// (docs/architecture.md, "Domain packages"): no gorm or SQL driver, no
// internal/model rows, no SpiceDB client, no Kratos table, and no generated
// API type outside the connect_handler*.go translators. Generated policy
// descriptors are the authorization port's vocabulary and stay allowed. The
// baseline counts files per domain package and category, and SpiceDB client
// and Kratos table references.
func TestDomainPackagesDependOnPortsRatchet(t *testing.T) {
	observed := map[string]int{}
	for _, file := range productionFiles(t, "internal") {
		if !inPipelineDomain(file.pkg) {
			continue
		}
		categories := map[string]bool{}
		for _, imported := range file.imports {
			switch {
			case imported == "gorm.io/gorm" || strings.HasPrefix(imported, "gorm.io/"):
				categories["gorm"] = true
			case imported == "github.com/lib/pq" || strings.HasPrefix(imported, "github.com/jackc/pgx"):
				categories["sql driver"] = true
			case imported == modulePath+"/internal/model":
				categories["model"] = true
			case strings.HasPrefix(imported, contractsImport) && imported != policyDescriptorsImport && !isConnectHandlerFile(file.path):
				categories["generated contracts"] = true
			}
		}
		for category := range categories {
			observed[file.pkg+" "+category]++
		}
		ast.Inspect(file.syntax, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.SelectorExpr:
				if file.importedAs(node, modulePath+"/internal/auth", "SpiceDBClient") {
					observed[file.pkg+" spicedb client"]++
				}
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					if value, err := strconv.Unquote(node.Value); err == nil && strings.Contains(value, "kratos.") {
						observed[file.pkg+" kratos tables"]++
					}
				}
			}
			return true
		})
	}
	requireRatchet(t, "domain_port_dependencies", observed)
}
