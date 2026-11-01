package architecture

import (
	"go/ast"
	"go/token"
	"path"
	"strings"
	"testing"
)

const (
	connectImport   = "connectrpc.com/connect"
	contractsImport = "github.com/echovisionlab/geul-event-contracts/gen/"
)

// pipelineDomains have moved to the use-case convention of
// docs/architecture.md: mutations run through internal/mutation, and only
// connect_handler*.go files know the transport. Add a domain here when its
// migration lands.
var pipelineDomains = []string{
	"internal/account",
	"internal/admin",
	"internal/ai",
	"internal/artist",
	"internal/audience",
	"internal/authentication",
	"internal/campaign",
	"internal/emailauthoring",
	"internal/emaildelivery",
	"internal/entityparticipant",
	"internal/form",
	"internal/label",
	"internal/legal",
	"internal/maptheme",
	"internal/mediaasset",
	"internal/member",
	"internal/menu",
	"internal/og",
	"internal/page",
	"internal/post",
	"internal/programevent",
	"internal/referencecatalog",
	"internal/release",
	"internal/series",
	"internal/sharelink",
	"internal/sitesettings",
	"internal/translation",
	"internal/work",
	"internal/worker",
}

func inPipelineDomain(pkg string) bool {
	for _, domain := range pipelineDomains {
		if underRoot(pkg, domain) {
			return true
		}
	}
	return false
}

func isConnectHandlerFile(file string) bool {
	return strings.HasPrefix(path.Base(file), "connect_handler")
}

// The MCP adapter calls use cases, not Connect handlers through a
// synthesized request.
func TestMCPConnectRequestRatchet(t *testing.T) {
	observed := map[string]int{}
	for _, file := range productionFiles(t, "internal/adapters/mcp") {
		ast.Inspect(file.syntax, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				function := call.Fun
				if index, ok := function.(*ast.IndexExpr); ok {
					function = index.X
				}
				if file.importedAs(function, connectImport, "NewRequest") {
					observed[file.path]++
				}
			}
			return true
		})
	}
	requireRatchet(t, "mcp_connect_new_request", observed)
}

// A migrated domain opens no transaction and takes no lifecycle lock
// itself; mutation.Run does. What remains are non-RPC lifecycle ports that
// other domains drive (deletion, provisioning).
func TestPipelineDomainsOpenNoTransactionsRatchet(t *testing.T) {
	observed := map[string]int{}
	for _, file := range productionFiles(t, "internal") {
		if !inPipelineDomain(file.pkg) {
			continue
		}
		ast.Inspect(file.syntax, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case selector.Sel.Name == "Transaction" && len(call.Args) != 0:
				if _, ok := call.Args[0].(*ast.FuncLit); ok {
					observed[file.path]++
				}
			case file.importedAs(selector, modulePath+"/internal/authzmutation", "Execute"),
				file.importedAs(selector, modulePath+"/internal/identitystate", "WithMutation"),
				file.importedAs(selector, modulePath+"/internal/authorizationtarget", "WithMutation"):
				observed[file.path]++
			}
			return true
		})
	}
	requireRatchet(t, "pipeline_domain_transactions", observed)
}

// Use cases of a migrated domain return domainerr errors and never see a
// Connect type; the connect_handler*.go translators map them.
func TestPipelineDomainUseCasesAreTransportFreeRatchet(t *testing.T) {
	observed := map[string]int{}
	for _, file := range productionFiles(t, "internal") {
		if !inPipelineDomain(file.pkg) || isConnectHandlerFile(file.path) {
			continue
		}
		for _, imported := range file.imports {
			if imported == connectImport || imported == modulePath+"/internal/errors" {
				observed[file.path+" imports "+imported]++
			}
		}
	}
	requireRatchet(t, "pipeline_domain_transport_imports", observed)
}

// internal/model is persistence; generated API types belong to the domain
// that maps them.
func TestModelImportsNoGeneratedContractsRatchet(t *testing.T) {
	observed := map[string]int{}
	for _, file := range productionFiles(t, "internal/model") {
		for _, imported := range file.imports {
			if strings.HasPrefix(imported, contractsImport) {
				observed[file.path+" imports "+imported]++
			}
		}
	}
	requireRatchet(t, "model_generated_imports", observed)
}

// A stored status is a domain constant (model.FormStatusDraft) mapped to the
// API enum in one function, not a generated enum name converted inline with
// String() or the _value map.
func TestGeneratedStatusNameConversionRatchet(t *testing.T) {
	observed := map[string]int{}
	for _, file := range productionFiles(t, "internal", "cmd") {
		generatedStatus := func(expression ast.Expr) bool {
			selector, ok := expression.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			ident, ok := selector.X.(*ast.Ident)
			return ok && strings.HasPrefix(file.imports[ident.Name], contractsImport) &&
				(strings.Contains(selector.Sel.Name, "_STATUS_") || strings.HasSuffix(selector.Sel.Name, "Status_value"))
		}
		ast.Inspect(file.syntax, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.CallExpr:
				if selector, ok := node.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "String" && len(node.Args) == 0 && generatedStatus(selector.X) {
					observed[file.pkg]++
				}
			case *ast.IndexExpr:
				if generatedStatus(node.X) {
					observed[file.pkg]++
				}
			}
			return true
		})
	}
	requireRatchet(t, "generated_status_name_conversions", observed)
}

// Audit is mandatory: services require an appender at construction, and
// tests pass audittest.Recorder. A nil check on an audit writer is a
// leftover of the optional-writer constructors.
func TestAuditWriterNilGuardRatchet(t *testing.T) {
	observed := map[string]int{}
	auditWriter := func(expression ast.Expr) bool {
		switch expression := expression.(type) {
		case *ast.Ident:
			return strings.Contains(strings.ToLower(expression.Name), "auditwriter")
		case *ast.SelectorExpr:
			return strings.Contains(strings.ToLower(expression.Sel.Name), "auditwriter")
		}
		return false
	}
	isNil := func(expression ast.Expr) bool {
		ident, ok := expression.(*ast.Ident)
		return ok && ident.Name == "nil"
	}
	for _, file := range productionFiles(t, "internal", "cmd") {
		ast.Inspect(file.syntax, func(node ast.Node) bool {
			binary, ok := node.(*ast.BinaryExpr)
			if ok && (binary.Op == token.EQL || binary.Op == token.NEQ) &&
				((auditWriter(binary.X) && isNil(binary.Y)) || (isNil(binary.X) && auditWriter(binary.Y))) {
				observed[file.pkg]++
			}
			return true
		})
	}
	requireRatchet(t, "audit_writer_nil_guards", observed)
}
