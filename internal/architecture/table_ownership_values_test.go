package architecture

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// ownershipValue describes only source-known strings, model types and finite
// composites. Calls, parameters and computed selectors remain unknown.
type ownershipValue struct {
	strings   map[string]bool
	models    map[string]bool
	fields    map[string]ownershipValue
	elements  []ownershipValue
	reference bool
}

func mergeOwnershipValues(values ...ownershipValue) ownershipValue {
	result := ownershipValue{strings: map[string]bool{}, models: map[string]bool{}, fields: map[string]ownershipValue{}}
	for _, value := range values {
		for name := range value.strings {
			result.strings[name] = true
		}
		for name := range value.models {
			result.models[name] = true
		}
		for name, field := range value.fields {
			result.fields[name] = mergeOwnershipValues(result.fields[name], field)
		}
		result.elements = append(result.elements, value.elements...)
		result.reference = result.reference || value.reference
	}
	return result
}

type ownershipSymbolKind uint8

const (
	ownershipVariable ownershipSymbolKind = iota
	ownershipConstant
	ownershipType
	ownershipFunction
	ownershipImport
)

// A symbol identifies one lexical declaration, independently of the Go
// parser's deprecated object-resolution pass. It carries no inferred Go type.
type ownershipSymbol struct {
	kind         ownershipSymbolKind
	name         string
	file         productionFile
	valueSpec    *ast.ValueSpec
	valueIndex   int
	typeSpec     *ast.TypeSpec
	typ          ast.Expr
	importPath   string
	packageLevel bool
	mutated      bool
	references   map[*ownershipSymbol]bool
	initializer  ast.Expr
}
type ownershipState map[*ownershipSymbol]ownershipValue
type ownershipPackage struct {
	symbols map[string]*ownershipSymbol
	uses    map[*ast.Ident]*ownershipSymbol
	escapes []*ast.CallExpr
}

func (p *ownershipPackage) symbol(name *ast.Ident) *ownershipSymbol {
	if name == nil {
		return nil
	}
	return p.uses[name]
}

type ownershipInspector struct {
	file      productionFile
	pkg       *ownershipPackage
	models    map[string]string
	state     ownershipState
	resolving map[*ownershipSymbol]bool
	record    func(string, string)
}

func inspectOwnershipCalls(files []productionFile, models map[string]string, record func(string, string)) {
	packages := collectOwnershipPackages(files)
	for _, file := range files {
		inspectOwnershipFile(file, packages[file.pkg], models, record)
	}
}

func collectOwnershipPackages(files []productionFile) map[string]*ownershipPackage {
	packages := map[string]*ownershipPackage{}
	for _, file := range files {
		pkg := packages[file.pkg]
		if pkg == nil {
			pkg = &ownershipPackage{symbols: map[string]*ownershipSymbol{}, uses: map[*ast.Ident]*ownershipSymbol{}}
			packages[file.pkg] = pkg
		}
		pkg.declareGlobals(file)
	}
	for _, file := range files {
		resolveOwnershipFile(file, packages[file.pkg])
	}
	for _, pkg := range packages {
		pkg.invalidateEscapedRegistries()
	}
	return packages
}

func (p *ownershipPackage) declareGlobals(file productionFile) {
	for _, declaration := range file.syntax.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Recv == nil {
				p.symbols[declaration.Name.Name] = &ownershipSymbol{kind: ownershipFunction, name: declaration.Name.Name, file: file, packageLevel: true}
			}
		case *ast.GenDecl:
			p.declareGlobalSpecs(file, declaration)
		}
	}
}

func (p *ownershipPackage) declareGlobalSpecs(file productionFile, declaration *ast.GenDecl) {
	for _, specification := range declaration.Specs {
		switch specification := specification.(type) {
		case *ast.TypeSpec:
			p.symbols[specification.Name.Name] = &ownershipSymbol{kind: ownershipType, name: specification.Name.Name, file: file, typeSpec: specification, packageLevel: true}
		case *ast.ValueSpec:
			kind := ownershipVariable
			if declaration.Tok == token.CONST {
				kind = ownershipConstant
			}
			for index, name := range specification.Names {
				p.symbols[name.Name] = &ownershipSymbol{kind: kind, name: name.Name, file: file, valueSpec: specification, valueIndex: index, packageLevel: true}
			}
		}
	}
}

func inspectOwnershipFile(file productionFile, pkg *ownershipPackage, models map[string]string, record func(string, string)) {
	inspector := ownershipInspector{file: file, pkg: pkg, models: models, state: ownershipState{}, resolving: map[*ownershipSymbol]bool{}, record: record}
	ast.Inspect(file.syntax, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok && inspector.directCall(call) {
			inspector.recordCall(call)
		}
		return true
	})
	for _, declaration := range file.syntax.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			inspector.state = ownershipState{}
			if declaration.Body != nil {
				inspector.statements(declaration.Body.List)
			}
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				if value, ok := specification.(*ast.ValueSpec); ok {
					for _, expression := range value.Values {
						inspector.expression(expression)
					}
				}
			}
		}
	}
}

func (i *ownershipInspector) modelType(expression ast.Expr) ownershipValue {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return i.modelType(expression.X)
	case *ast.StarExpr:
		return i.modelType(expression.X)
	case *ast.ArrayType:
		return i.modelType(expression.Elt)
	case *ast.SelectorExpr:
		if name, ok := expression.X.(*ast.Ident); ok {
			imported := i.pkg.symbol(name)
			if imported != nil && imported.kind == ownershipImport && imported.importPath == modulePath+"/internal/model" {
				if _, known := i.models[expression.Sel.Name]; known {
					return ownershipValue{models: map[string]bool{expression.Sel.Name: true}}
				}
			}
		}
	case *ast.Ident:
		symbol := i.pkg.symbol(expression)
		if symbol != nil && symbol.typeSpec != nil && symbol.typeSpec.Assign.IsValid() {
			return i.aliasModels(symbol)
		}
	}
	return ownershipValue{}
}

func (i *ownershipInspector) aliasModels(symbol *ownershipSymbol) ownershipValue {
	if i.resolving[symbol] {
		return ownershipValue{}
	}
	i.resolving[symbol] = true
	file := i.file
	i.file = symbol.file
	value := i.modelType(symbol.typeSpec.Type)
	i.file = file
	delete(i.resolving, symbol)
	return value
}

func (i *ownershipInspector) value(expression ast.Expr) ownershipValue {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind == token.STRING {
			if value, err := strconv.Unquote(expression.Value); err == nil {
				return ownershipValue{strings: map[string]bool{value: true}}
			}
		}
	case *ast.ParenExpr:
		return i.value(expression.X)
	case *ast.UnaryExpr:
		if expression.Op == token.AND {
			value := i.value(expression.X)
			value.reference = true
			return value
		}
	case *ast.StarExpr:
		return i.value(expression.X)
	case *ast.BinaryExpr:
		if expression.Op == token.ADD {
			left, right := i.value(expression.X), i.value(expression.Y)
			result := ownershipValue{strings: map[string]bool{}}
			for a := range left.strings {
				for b := range right.strings {
					result.strings[a+b] = true
				}
			}
			return result
		}
	case *ast.Ident:
		return i.identifierValue(expression)
	case *ast.CompositeLit:
		return i.composite(expression, expression.Type)
	case *ast.SelectorExpr:
		return i.value(expression.X).fields[expression.Sel.Name]
	case *ast.IndexExpr:
		return mergeOwnershipValues(i.value(expression.X).elements...)
	case *ast.CallExpr:
		if name, ok := expression.Fun.(*ast.Ident); ok && i.pkg.symbol(name) == nil && len(expression.Args) > 0 {
			return i.builtinValue(name.Name, expression.Args)
		}
		// Explicit model conversions have a known result type; ordinary
		// function return values and generic instantiations do not.
		return i.modelType(expression.Fun)
	}
	return ownershipValue{}
}

func (i *ownershipInspector) builtinValue(name string, args []ast.Expr) ownershipValue {
	switch name {
	case "new", "make":
		return i.modelType(args[0])
	case "append":
		value := i.value(args[0])
		for _, argument := range args[1:] {
			value.elements = append(value.elements, i.value(argument))
		}
		return value
	default:
		return ownershipValue{}
	}
}

func (i *ownershipInspector) identifierValue(expression *ast.Ident) ownershipValue {
	symbol := i.pkg.symbol(expression)
	if symbol == nil {
		return ownershipValue{}
	}
	if value, found := i.state[symbol]; found {
		return value
	}
	if symbol.packageLevel && symbol.valueSpec != nil {
		if symbol.mutated {
			return i.stableModels(symbol, map[*ownershipSymbol]bool{})
		}
		if i.resolving[symbol] {
			return ownershipValue{}
		}
		i.resolving[symbol] = true
		file := i.file
		i.file = symbol.file
		value := i.declaredValue(symbol.valueSpec, symbol.valueIndex)
		i.file = file
		delete(i.resolving, symbol)
		return value
	}
	if symbol.typ != nil {
		return i.modelType(symbol.typ)
	}
	return ownershipValue{}
}

func (i *ownershipInspector) declaredValue(spec *ast.ValueSpec, index int) ownershipValue {
	if spec.Type != nil {
		if value := i.modelType(spec.Type); len(value.models) != 0 {
			return value
		}
	}
	if index < len(spec.Values) {
		return i.value(spec.Values[index])
	}
	return ownershipValue{}
}

func (i *ownershipInspector) expression(expression ast.Expr) {
	ast.Inspect(expression, func(node ast.Node) bool {
		if literal, ok := node.(*ast.FuncLit); ok {
			state := i.state
			i.state = cloneOwnershipState(state)
			i.statements(literal.Body.List)
			i.state = state
			for symbol := range i.closureWrites(literal) {
				i.state[symbol] = i.stableModels(symbol, map[*ownershipSymbol]bool{})
			}
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if !i.directCall(call) {
			i.recordCall(call)
		}
		return true
	})
}

func cloneOwnershipState(state ownershipState) ownershipState {
	result := ownershipState{}
	for key, value := range state {
		result[key] = value
	}
	return result
}
func (i *ownershipInspector) branches(states []ownershipState) {
	if len(states) == 0 {
		return
	}
	result := ownershipState{}
	for _, state := range states {
		for key, value := range state {
			result[key] = mergeOwnershipValues(result[key], value)
		}
	}
	i.state = result
}
func (i *ownershipInspector) statements(statements []ast.Stmt) bool {
	for _, statement := range statements {
		if !i.statement(statement) {
			return false
		}
	}
	return true
}
func (i *ownershipInspector) statement(statement ast.Stmt) bool {
	switch statement := statement.(type) {
	case *ast.BlockStmt:
		return i.statements(statement.List)
	case *ast.ExprStmt:
		i.expression(statement.X)
	case *ast.ReturnStmt:
		for _, expression := range statement.Results {
			i.expression(expression)
		}
		return false
	case *ast.AssignStmt:
		i.assignment(statement)
	case *ast.DeclStmt:
		i.declaration(statement)
	case *ast.IfStmt:
		return i.conditional(statement)
	case *ast.SwitchStmt:
		return i.switchStatement(statement)
	case *ast.RangeStmt:
		i.rangeStatement(statement)
	case *ast.ForStmt:
		i.forStatement(statement)
	case *ast.DeferStmt:
		i.expression(statement.Call)
	case *ast.GoStmt:
		i.expression(statement.Call)
	case *ast.LabeledStmt:
		return i.statement(statement.Stmt)
	default:
		ast.Inspect(statement, func(node ast.Node) bool {
			if expression, ok := node.(ast.Expr); ok {
				i.expression(expression)
				return false
			}
			return true
		})
	}
	return true
}

func ownershipRoot(expression ast.Expr) *ast.Ident {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression
	case *ast.SelectorExpr:
		return ownershipRoot(expression.X)
	case *ast.IndexExpr:
		return ownershipRoot(expression.X)
	case *ast.StarExpr:
		return ownershipRoot(expression.X)
	default:
		return nil
	}
}

func (i *ownershipInspector) assignment(statement *ast.AssignStmt) {
	values := make([]ownershipValue, len(statement.Rhs))
	for index, expression := range statement.Rhs {
		i.expression(expression)
		values[index] = i.value(expression)
	}
	for index, expression := range statement.Lhs {
		name := ownershipRoot(expression)
		if name == nil || i.pkg.symbol(name) == nil {
			continue
		}
		if _, direct := expression.(*ast.Ident); !direct {
			i.state[i.pkg.symbol(name)] = ownershipValue{models: i.value(name).models}
			continue
		}
		value := ownershipValue{}
		if index < len(values) {
			value = values[index]
		}
		i.state[i.pkg.symbol(name)] = value
	}
}

func (i *ownershipInspector) declaration(statement *ast.DeclStmt) {
	declaration, ok := statement.Decl.(*ast.GenDecl)
	if !ok {
		return
	}
	for _, specification := range declaration.Specs {
		spec, ok := specification.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for _, expression := range spec.Values {
			i.expression(expression)
		}
		for index, name := range spec.Names {
			i.state[i.pkg.symbol(name)] = i.declaredValue(spec, index)
		}
	}
}

func (i *ownershipInspector) conditional(statement *ast.IfStmt) bool {
	if statement.Init != nil {
		i.statement(statement.Init)
	}
	i.expression(statement.Cond)
	before := cloneOwnershipState(i.state)
	var states []ownershipState
	i.state = cloneOwnershipState(before)
	if i.statements(statement.Body.List) {
		states = append(states, i.state)
	}
	i.state = cloneOwnershipState(before)
	if statement.Else == nil || i.statement(statement.Else) {
		states = append(states, i.state)
	}
	i.branches(states)
	return len(states) != 0
}

func (i *ownershipInspector) switchStatement(statement *ast.SwitchStmt) bool {
	if statement.Init != nil {
		i.statement(statement.Init)
	}
	if statement.Tag != nil {
		i.expression(statement.Tag)
	}
	before := cloneOwnershipState(i.state)
	var states []ownershipState
	hasDefault := false
	for _, item := range statement.Body.List {
		clause := item.(*ast.CaseClause)
		if len(clause.List) == 0 {
			hasDefault = true
		}
		i.state = cloneOwnershipState(before)
		for _, expression := range clause.List {
			i.expression(expression)
		}
		if i.statements(clause.Body) {
			states = append(states, i.state)
		}
	}
	if !hasDefault {
		states = append(states, before)
	}
	i.branches(states)
	return len(states) != 0
}

func (i *ownershipInspector) rangeStatement(statement *ast.RangeStmt) {
	i.expression(statement.X)
	value := i.value(statement.X)
	before := cloneOwnershipState(i.state)
	if name, ok := statement.Key.(*ast.Ident); ok {
		i.state[i.pkg.symbol(name)] = ownershipValue{}
	}
	if name, ok := statement.Value.(*ast.Ident); ok {
		i.state[i.pkg.symbol(name)] = mergeOwnershipValues(append(value.elements, ownershipValue{models: value.models})...)
	}
	i.statements(statement.Body.List)
	i.branches([]ownershipState{before, i.state})
}

func (i *ownershipInspector) forStatement(statement *ast.ForStmt) {
	if statement.Init != nil {
		i.statement(statement.Init)
	}
	if statement.Cond != nil {
		i.expression(statement.Cond)
	}
	before := cloneOwnershipState(i.state)
	i.statements(statement.Body.List)
	if statement.Post != nil {
		i.statement(statement.Post)
	}
	i.branches([]ownershipState{before, i.state})
}

func (i *ownershipInspector) underlying(expression ast.Expr) ast.Expr {
	switch expression := expression.(type) {
	case *ast.Ident:
		symbol := i.pkg.symbol(expression)
		if symbol != nil && symbol.typeSpec != nil {
			return i.underlying(symbol.typeSpec.Type)
		}
	case *ast.ParenExpr:
		return i.underlying(expression.X)
	}
	return expression
}

func (i *ownershipInspector) valueWithType(expression ast.Expr, expected ast.Expr) ownershipValue {
	if literal, ok := expression.(*ast.CompositeLit); ok {
		return i.composite(literal, expected)
	}
	return i.value(expression)
}

func (i *ownershipInspector) composite(literal *ast.CompositeLit, expected ast.Expr) ownershipValue {
	typ := literal.Type
	if typ == nil {
		typ = expected
	}
	result := i.modelType(typ)
	result.fields = map[string]ownershipValue{}
	switch typ := i.underlying(typ).(type) {
	case *ast.ArrayType:
		for _, element := range literal.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				element = pair.Value
			}
			result.elements = append(result.elements, i.valueWithType(element, typ.Elt))
		}
	case *ast.MapType:
		for _, element := range literal.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				result.elements = append(result.elements, i.valueWithType(pair.Value, typ.Value))
			}
		}
	case *ast.StructType:
		i.structFields(literal, typ, &result)
	default:
		for _, element := range literal.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				if name, ok := pair.Key.(*ast.Ident); ok {
					result.fields[name.Name] = i.value(pair.Value)
				}
			}
		}
	}
	return result
}

func (i *ownershipInspector) structFields(literal *ast.CompositeLit, typ *ast.StructType, result *ownershipValue) {
	var names []string
	types := map[string]ast.Expr{}
	for _, field := range typ.Fields.List {
		for _, name := range field.Names {
			names = append(names, name.Name)
			types[name.Name] = field.Type
		}
	}
	for index, element := range literal.Elts {
		name := ""
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			if ident, ok := pair.Key.(*ast.Ident); ok {
				name = ident.Name
			}
			element = pair.Value
		} else if index < len(names) {
			name = names[index]
		}
		if name != "" {
			result.fields[name] = i.valueWithType(element, types[name])
		}
	}
}

// ownershipScope resolves names only by their lexical declaration. Imported
// packages, type parameters and unknown values keep distinct symbols.
type ownershipScope struct {
	parent  *ownershipScope
	symbols map[string]*ownershipSymbol
}

func newOwnershipScope(parent *ownershipScope) *ownershipScope {
	return &ownershipScope{parent: parent, symbols: map[string]*ownershipSymbol{}}
}

func (s *ownershipScope) lookup(name string) *ownershipSymbol {
	if name == "_" {
		return nil
	}
	for current := s; current != nil; current = current.parent {
		if symbol := current.symbols[name]; symbol != nil {
			return symbol
		}
	}
	return nil
}

type ownershipResolver struct {
	file productionFile
	pkg  *ownershipPackage
}

func resolveOwnershipFile(file productionFile, pkg *ownershipPackage) {
	resolver := ownershipResolver{file: file, pkg: pkg}
	packageScope := &ownershipScope{symbols: pkg.symbols}
	fileScope := newOwnershipScope(packageScope)
	for name, imported := range file.imports {
		fileScope.symbols[name] = &ownershipSymbol{kind: ownershipImport, name: name, file: file, importPath: imported}
	}
	for _, declaration := range file.syntax.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			resolver.general(declaration, fileScope, true)
		case *ast.FuncDecl:
			resolver.function(declaration.Type, declaration.Recv, declaration.Body, fileScope)
		}
	}
}

func (r *ownershipResolver) declare(scope *ownershipScope, name *ast.Ident, kind ownershipSymbolKind) *ownershipSymbol {
	if name == nil || name.Name == "_" {
		return nil
	}
	symbol := &ownershipSymbol{kind: kind, name: name.Name, file: r.file}
	scope.symbols[name.Name] = symbol
	r.pkg.uses[name] = symbol
	return symbol
}

func (r *ownershipResolver) identifier(name *ast.Ident, scope *ownershipScope) {
	if name != nil {
		r.pkg.uses[name] = scope.lookup(name.Name)
	}
}

func (r *ownershipResolver) expressions(expressions []ast.Expr, scope *ownershipScope) {
	for _, expression := range expressions {
		r.expression(expression, scope)
	}
}

func (r *ownershipResolver) expression(expression ast.Expr, scope *ownershipScope) {
	switch expression := expression.(type) {
	case *ast.Ident:
		r.identifier(expression, scope)
	case *ast.ParenExpr:
		r.expression(expression.X, scope)
	case *ast.StarExpr:
		r.expression(expression.X, scope)
	case *ast.UnaryExpr:
		r.expression(expression.X, scope)
	case *ast.SelectorExpr:
		r.expression(expression.X, scope)
	case *ast.IndexExpr:
		r.expression(expression.X, scope)
		r.expression(expression.Index, scope)
	case *ast.IndexListExpr:
		r.expression(expression.X, scope)
		r.expressions(expression.Indices, scope)
	case *ast.SliceExpr:
		r.expression(expression.X, scope)
		r.expression(expression.Low, scope)
		r.expression(expression.High, scope)
		r.expression(expression.Max, scope)
	case *ast.TypeAssertExpr:
		r.expression(expression.X, scope)
		r.expression(expression.Type, scope)
	case *ast.CallExpr:
		r.expression(expression.Fun, scope)
		r.expressions(expression.Args, scope)
		r.pkg.escapes = append(r.pkg.escapes, expression)
	case *ast.BinaryExpr:
		r.expression(expression.X, scope)
		r.expression(expression.Y, scope)
	case *ast.KeyValueExpr:
		r.expression(expression.Key, scope)
		r.expression(expression.Value, scope)
	case *ast.CompositeLit:
		r.expression(expression.Type, scope)
		r.expressions(expression.Elts, scope)
	case *ast.ArrayType:
		r.expression(expression.Len, scope)
		r.expression(expression.Elt, scope)
	case *ast.MapType:
		r.expression(expression.Key, scope)
		r.expression(expression.Value, scope)
	case *ast.ChanType:
		r.expression(expression.Value, scope)
	case *ast.StructType:
		r.fieldTypes(expression.Fields, scope)
	case *ast.InterfaceType:
		r.fieldTypes(expression.Methods, scope)
	case *ast.FuncType:
		r.function(expression, nil, nil, scope)
	case *ast.FuncLit:
		r.function(expression.Type, nil, expression.Body, scope)
	case *ast.Ellipsis:
		r.expression(expression.Elt, scope)
	}
}

func (r *ownershipResolver) general(declaration *ast.GenDecl, scope *ownershipScope, global bool) {
	for _, specification := range declaration.Specs {
		switch specification := specification.(type) {
		case *ast.TypeSpec:
			r.typeDeclaration(specification, scope, global)
		case *ast.ValueSpec:
			r.valueDeclaration(specification, declaration.Tok, scope, global)
		}
	}
}

func (r *ownershipResolver) typeDeclaration(spec *ast.TypeSpec, scope *ownershipScope, global bool) {
	symbol := r.pkg.symbols[spec.Name.Name]
	if !global {
		symbol = r.declare(scope, spec.Name, ownershipType)
	}
	if symbol != nil {
		symbol.typeSpec = spec
		r.pkg.uses[spec.Name] = symbol
	}
	typeScope := newOwnershipScope(scope)
	r.typeParameters(spec.TypeParams, typeScope)
	r.expression(spec.Type, typeScope)
}

func (r *ownershipResolver) valueDeclaration(spec *ast.ValueSpec, declaration token.Token, scope *ownershipScope, global bool) {
	// The scope of a local value starts after its initializer.
	r.expression(spec.Type, scope)
	r.expressions(spec.Values, scope)
	kind := ownershipVariable
	if declaration == token.CONST {
		kind = ownershipConstant
	}
	for index, name := range spec.Names {
		symbol := r.pkg.symbols[name.Name]
		if !global {
			symbol = r.declare(scope, name, kind)
		}
		if symbol == nil {
			continue
		}
		symbol.valueSpec, symbol.valueIndex = spec, index
		r.pkg.uses[name] = symbol
		if index < len(spec.Values) {
			r.addReferences(symbol, spec.Values[index])
		}
	}
}

func (r *ownershipResolver) typeParameters(fields *ast.FieldList, scope *ownershipScope) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			r.declare(scope, name, ownershipType)
		}
	}
	r.fieldTypes(fields, scope)
}

func (r *ownershipResolver) fieldTypes(fields *ast.FieldList, scope *ownershipScope) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		r.expression(field.Type, scope)
	}
}

func (r *ownershipResolver) fieldNames(fields *ast.FieldList, scope *ownershipScope) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			if symbol := r.declare(scope, name, ownershipVariable); symbol != nil {
				symbol.typ = field.Type
			}
		}
	}
}

func (r *ownershipResolver) function(typ *ast.FuncType, receiver *ast.FieldList, body *ast.BlockStmt, outer *ownershipScope) {
	scope := newOwnershipScope(outer)
	if receiver != nil {
		for _, field := range receiver.List {
			r.receiverParameters(field.Type, scope)
		}
	}
	r.typeParameters(typ.TypeParams, scope)
	// Value parameter names begin in the body, so they do not shadow an
	// imported package while another parameter's type is being resolved.
	r.fieldTypes(receiver, scope)
	r.fieldTypes(typ.Params, scope)
	r.fieldTypes(typ.Results, scope)
	r.fieldNames(receiver, scope)
	r.fieldNames(typ.Params, scope)
	r.fieldNames(typ.Results, scope)
	if body != nil {
		r.statements(body.List, scope)
	}
}

func (r *ownershipResolver) receiverParameters(expression ast.Expr, scope *ownershipScope) {
	switch expression := expression.(type) {
	case *ast.StarExpr:
		r.receiverParameters(expression.X, scope)
	case *ast.IndexExpr:
		if name, ok := expression.Index.(*ast.Ident); ok {
			r.declare(scope, name, ownershipType)
		}
	case *ast.IndexListExpr:
		for _, index := range expression.Indices {
			if name, ok := index.(*ast.Ident); ok {
				r.declare(scope, name, ownershipType)
			}
		}
	}
}

func (r *ownershipResolver) statements(statements []ast.Stmt, scope *ownershipScope) {
	for _, statement := range statements {
		r.statement(statement, scope)
	}
}

func (r *ownershipResolver) statement(statement ast.Stmt, scope *ownershipScope) {
	switch statement := statement.(type) {
	case *ast.BlockStmt:
		r.statements(statement.List, newOwnershipScope(scope))
	case *ast.ExprStmt:
		r.expression(statement.X, scope)
	case *ast.AssignStmt:
		r.assignment(statement, scope)
	case *ast.DeclStmt:
		if declaration, ok := statement.Decl.(*ast.GenDecl); ok {
			r.general(declaration, scope, false)
		}
	case *ast.IfStmt:
		r.conditional(statement, scope)
	case *ast.ForStmt:
		r.forStatement(statement, scope)
	case *ast.RangeStmt:
		r.rangeStatement(statement, scope)
	case *ast.SwitchStmt:
		r.switchStatement(statement, scope)
	case *ast.TypeSwitchStmt:
		r.typeSwitch(statement, scope)
	case *ast.SelectStmt:
		r.selectStatement(statement, scope)
	case *ast.ReturnStmt:
		r.expressions(statement.Results, scope)
	case *ast.GoStmt:
		r.expression(statement.Call, scope)
	case *ast.DeferStmt:
		r.expression(statement.Call, scope)
	case *ast.SendStmt:
		r.expression(statement.Chan, scope)
		r.expression(statement.Value, scope)
	case *ast.IncDecStmt:
		r.expression(statement.X, scope)
	case *ast.LabeledStmt:
		r.statement(statement.Stmt, scope)
	}
}

func (r *ownershipResolver) assignment(statement *ast.AssignStmt, scope *ownershipScope) {
	r.expressions(statement.Rhs, scope)
	for index, left := range statement.Lhs {
		name, direct := left.(*ast.Ident)
		if statement.Tok == token.DEFINE && direct {
			if symbol := scope.symbols[name.Name]; symbol != nil {
				r.pkg.uses[name] = symbol
			} else {
				r.declare(scope, name, ownershipVariable)
			}
		} else {
			r.expression(left, scope)
			r.markPackageMutation(left)
		}
		if direct && index < len(statement.Rhs) {
			symbol := r.pkg.symbol(name)
			if symbol != nil && symbol.initializer == nil && symbol.valueSpec == nil && symbol.typ == nil {
				symbol.initializer = statement.Rhs[index]
			}
			r.addReferences(symbol, statement.Rhs[index])
		}
	}
}

func (r *ownershipResolver) markPackageMutation(expression ast.Expr) {
	root := ownershipRoot(expression)
	symbol := r.pkg.symbol(root)
	if symbol != nil && symbol.packageLevel && symbol.kind == ownershipVariable {
		symbol.mutated = true
	}
	if _, direct := expression.(*ast.Ident); !direct {
		for source := range r.pkg.referenceOrigins(root) {
			source.mutated = true
		}
	}
}

func (r *ownershipResolver) addReferences(symbol *ownershipSymbol, expression ast.Expr) {
	if symbol == nil {
		return
	}
	if symbol.references == nil {
		symbol.references = map[*ownershipSymbol]bool{}
	}
	for source := range r.pkg.referenceOrigins(expression) {
		symbol.references[source] = true
	}
}

func (r *ownershipResolver) conditional(statement *ast.IfStmt, outer *ownershipScope) {
	scope := newOwnershipScope(outer)
	r.statement(statement.Init, scope)
	r.expression(statement.Cond, scope)
	r.statement(statement.Body, scope)
	r.statement(statement.Else, scope)
}

func (r *ownershipResolver) forStatement(statement *ast.ForStmt, outer *ownershipScope) {
	scope := newOwnershipScope(outer)
	r.statement(statement.Init, scope)
	r.expression(statement.Cond, scope)
	r.statement(statement.Post, scope)
	r.statement(statement.Body, scope)
}

func (r *ownershipResolver) rangeStatement(statement *ast.RangeStmt, outer *ownershipScope) {
	r.expression(statement.X, outer)
	scope := newOwnershipScope(outer)
	r.rangeName(statement.Key, statement.Tok, scope)
	r.rangeName(statement.Value, statement.Tok, scope)
	r.statement(statement.Body, scope)
}

func (r *ownershipResolver) rangeName(expression ast.Expr, operation token.Token, scope *ownershipScope) {
	if name, ok := expression.(*ast.Ident); ok && operation == token.DEFINE {
		r.declare(scope, name, ownershipVariable)
	} else {
		r.expression(expression, scope)
	}
}

func (r *ownershipResolver) switchStatement(statement *ast.SwitchStmt, outer *ownershipScope) {
	scope := newOwnershipScope(outer)
	r.statement(statement.Init, scope)
	r.expression(statement.Tag, scope)
	for _, item := range statement.Body.List {
		clause := item.(*ast.CaseClause)
		clauseScope := newOwnershipScope(scope)
		r.expressions(clause.List, clauseScope)
		r.statements(clause.Body, clauseScope)
	}
}

func (r *ownershipResolver) typeSwitch(statement *ast.TypeSwitchStmt, outer *ownershipScope) {
	scope := newOwnershipScope(outer)
	r.statement(statement.Init, scope)
	assignment, declares := statement.Assign.(*ast.AssignStmt)
	if declares {
		r.expressions(assignment.Rhs, scope)
	} else {
		r.statement(statement.Assign, scope)
	}
	for _, item := range statement.Body.List {
		clause := item.(*ast.CaseClause)
		clauseScope := newOwnershipScope(scope)
		r.expressions(clause.List, clauseScope)
		if declares {
			r.typeSwitchNames(assignment, clauseScope)
		}
		r.statements(clause.Body, clauseScope)
	}
}

func (r *ownershipResolver) typeSwitchNames(assignment *ast.AssignStmt, scope *ownershipScope) {
	for _, left := range assignment.Lhs {
		if name, ok := left.(*ast.Ident); ok {
			r.declare(scope, name, ownershipVariable)
		}
	}
}

func (r *ownershipResolver) selectStatement(statement *ast.SelectStmt, outer *ownershipScope) {
	for _, item := range statement.Body.List {
		clause := item.(*ast.CommClause)
		scope := newOwnershipScope(outer)
		r.statement(clause.Comm, scope)
		r.statements(clause.Body, scope)
	}
}

// Direct syntax is independent of the value walk's control-flow approximation.
// The value walk skips these calls so labels cannot drop or duplicate them.
func (i *ownershipInspector) directCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	switch selector.Sel.Name {
	case "Table":
		argument := call.Args[0]
		for {
			parenthesized, ok := argument.(*ast.ParenExpr)
			if !ok {
				break
			}
			argument = parenthesized.X
		}
		literal, ok := argument.(*ast.BasicLit)
		return ok && literal.Kind == token.STRING
	case "Model", "Create", "Delete":
		return i.directModel(call.Args[0])
	default:
		return false
	}
}

func (i *ownershipInspector) directModel(expression ast.Expr) bool {
	switch expression := expression.(type) {
	case *ast.ParenExpr:
		return i.directModel(expression.X)
	case *ast.UnaryExpr:
		return expression.Op == token.AND && i.directModel(expression.X)
	case *ast.StarExpr:
		return i.directModel(expression.X)
	case *ast.CompositeLit:
		return len(i.modelType(expression.Type).models) > 0
	case *ast.CallExpr:
		if name, ok := expression.Fun.(*ast.Ident); ok && i.pkg.symbol(name) == nil && len(expression.Args) > 0 {
			if name.Name == "new" || name.Name == "make" {
				return len(i.modelType(expression.Args[0]).models) > 0
			}
		}
		return len(i.modelType(expression.Fun).models) > 0
	default:
		return false
	}
}

func (i *ownershipInspector) recordCall(call *ast.CallExpr) {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) == 0 {
		return
	}
	value := i.value(call.Args[0])
	switch selector.Sel.Name {
	case "Table":
		for name := range value.strings {
			if strings.HasPrefix(name, "kratos.") {
				continue
			}
			table, _, _ := strings.Cut(strings.TrimPrefix(name, "public."), " ")
			i.record(i.file.pkg, table)
		}
	case "Model", "Create", "Delete":
		for model := range value.models {
			i.record(i.file.pkg, i.models[model])
		}
	}
}

func (i *ownershipInspector) closureLocals(literal *ast.FuncLit) map[*ownershipSymbol]bool {
	locals := map[*ownershipSymbol]bool{}
	add := func(name *ast.Ident) {
		if symbol := i.pkg.symbol(name); symbol != nil {
			locals[symbol] = true
		}
	}
	ast.Inspect(literal, func(node ast.Node) bool {
		for _, name := range ownershipDeclarationNames(node) {
			add(name)
		}
		return true
	})
	return locals
}

func ownershipDeclarationNames(node ast.Node) []*ast.Ident {
	switch node := node.(type) {
	case *ast.Field:
		return node.Names
	case *ast.ValueSpec:
		return node.Names
	case *ast.AssignStmt:
		if node.Tok == token.DEFINE {
			return ownershipDeclaredIdentifiers(node.Lhs)
		}
	case *ast.RangeStmt:
		if node.Tok == token.DEFINE {
			return ownershipDeclaredIdentifiers([]ast.Expr{node.Key, node.Value})
		}
	}
	return nil
}

func ownershipDeclaredIdentifiers(expressions []ast.Expr) []*ast.Ident {
	var names []*ast.Ident
	for _, expression := range expressions {
		if name, ok := expression.(*ast.Ident); ok {
			names = append(names, name)
		}
	}
	return names
}

func (i *ownershipInspector) closureWrites(literal *ast.FuncLit) map[*ownershipSymbol]bool {
	locals := i.closureLocals(literal)
	writes := map[*ownershipSymbol]bool{}
	add := func(expression ast.Expr) {
		symbol := i.pkg.symbol(ownershipRoot(expression))
		if symbol != nil && symbol.kind == ownershipVariable && !locals[symbol] {
			writes[symbol] = true
		}
	}
	ast.Inspect(literal.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for _, left := range node.Lhs {
				add(left)
			}
		case *ast.IncDecStmt:
			add(node.X)
		case *ast.UnaryExpr:
			if node.Op == token.AND {
				add(node.X)
			}
		case *ast.CallExpr:
			if !i.pkg.readonlyBuiltin(node) {
				for _, argument := range node.Args {
					if !ownershipScalar(i.value(argument)) {
						add(argument)
					}
				}
			}
		}
		return true
	})
	return writes
}

// Only reference provenance is propagated here. Function results remain
// unknown; a call receiving a mutable registry invalidates that registry.
func (p *ownershipPackage) referenceOrigins(expression ast.Expr) map[*ownershipSymbol]bool {
	return p.referenceOriginsVisiting(expression, map[*ownershipSymbol]bool{})
}

func (p *ownershipPackage) referenceOriginsVisiting(expression ast.Expr, visiting map[*ownershipSymbol]bool) map[*ownershipSymbol]bool {
	result := map[*ownershipSymbol]bool{}
	switch expression := expression.(type) {
	case *ast.Ident:
		return p.symbolReferenceOrigins(p.symbol(expression), visiting)
	case *ast.ParenExpr:
		return p.referenceOriginsVisiting(expression.X, visiting)
	case *ast.UnaryExpr:
		return p.referenceOriginsVisiting(expression.X, visiting)
	case *ast.StarExpr:
		return p.referenceOriginsVisiting(expression.X, visiting)
	case *ast.SelectorExpr:
		return p.referenceOriginsVisiting(expression.X, visiting)
	case *ast.IndexExpr:
		return p.referenceOriginsVisiting(expression.X, visiting)
	case *ast.SliceExpr:
		return p.referenceOriginsVisiting(expression.X, visiting)
	case *ast.CallExpr:
		if name, ok := expression.Fun.(*ast.Ident); ok && p.symbol(name) == nil && name.Name == "append" && len(expression.Args) > 0 {
			return p.referenceOriginsVisiting(expression.Args[0], visiting)
		}
	case *ast.CompositeLit:
		for _, element := range expression.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				element = pair.Value
			}
			for source := range p.referenceOriginsVisiting(element, visiting) {
				result[source] = true
			}
		}
	}
	return result
}

func (p *ownershipPackage) symbolReferenceOrigins(symbol *ownershipSymbol, visiting map[*ownershipSymbol]bool) map[*ownershipSymbol]bool {
	result := map[*ownershipSymbol]bool{}
	if symbol == nil || visiting[symbol] {
		return result
	}
	visiting[symbol] = true
	defer delete(visiting, symbol)
	if p.mutableRegistry(symbol) {
		result[symbol] = true
	}
	for source := range symbol.references {
		result[source] = true
	}
	if symbol.packageLevel && symbol.valueSpec != nil && symbol.valueIndex < len(symbol.valueSpec.Values) {
		initializer := symbol.valueSpec.Values[symbol.valueIndex]
		for source := range p.referenceOriginsVisiting(initializer, visiting) {
			result[source] = true
		}
	}
	return result
}

func (p *ownershipPackage) mutableRegistry(symbol *ownershipSymbol) bool {
	if !symbol.packageLevel || symbol.kind != ownershipVariable || symbol.valueSpec == nil {
		return false
	}
	if p.referenceType(symbol.valueSpec.Type) {
		return true
	}
	if symbol.valueIndex >= len(symbol.valueSpec.Values) {
		return false
	}
	switch expression := symbol.valueSpec.Values[symbol.valueIndex].(type) {
	case *ast.CompositeLit:
		return true
	case *ast.UnaryExpr:
		return expression.Op == token.AND
	case *ast.CallExpr:
		name, ok := expression.Fun.(*ast.Ident)
		return ok && p.symbol(name) == nil && (name.Name == "new" || name.Name == "make")
	default:
		return false
	}
}

func (p *ownershipPackage) referenceType(expression ast.Expr) bool {
	switch expression := expression.(type) {
	case *ast.ArrayType, *ast.MapType, *ast.StarExpr, *ast.StructType:
		return true
	case *ast.ParenExpr:
		return p.referenceType(expression.X)
	case *ast.Ident:
		symbol := p.symbol(expression)
		if symbol != nil && symbol.typeSpec != nil {
			return p.referenceType(symbol.typeSpec.Type)
		}
	}
	return false
}

func (p *ownershipPackage) invalidateEscapedRegistries() {
	for _, call := range p.escapes {
		if p.readonlyBuiltin(call) {
			continue
		}
		for _, argument := range call.Args {
			for source := range p.referenceOrigins(argument) {
				if !p.referenceScalar(argument, source) {
					source.mutated = true
				}
			}
		}
	}
}

func (p *ownershipPackage) referenceScalar(expression ast.Expr, source *ownershipSymbol) bool {
	if source.mutated || source.valueSpec == nil {
		return false
	}
	// Passing a whole reference, including a pointer to a scalar, can mutate
	// it. Only a proven scalar selected from the registry is copied by value.
	switch expression.(type) {
	case *ast.SelectorExpr, *ast.IndexExpr:
	default:
		return false
	}
	inspector := ownershipInspector{file: source.file, pkg: p, state: ownershipState{}, resolving: map[*ownershipSymbol]bool{}}
	value := inspector.declaredValue(source.valueSpec, source.valueIndex)
	values := ownershipReferencePath(expression, []ownershipValue{value})
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !ownershipScalar(value) {
			return false
		}
	}
	return true
}

func ownershipReferencePath(expression ast.Expr, roots []ownershipValue) []ownershipValue {
	switch expression := expression.(type) {
	case *ast.Ident:
		return roots
	case *ast.ParenExpr:
		return ownershipReferencePath(expression.X, roots)
	case *ast.SelectorExpr:
		var values []ownershipValue
		for _, value := range ownershipReferencePath(expression.X, roots) {
			values = append(values, value.fields[expression.Sel.Name])
		}
		return values
	case *ast.IndexExpr:
		var values []ownershipValue
		for _, value := range ownershipReferencePath(expression.X, roots) {
			if len(value.elements) == 0 {
				values = append(values, ownershipValue{})
			} else {
				values = append(values, value.elements...)
			}
		}
		return values
	default:
		return nil
	}
}

func (p *ownershipPackage) readonlyBuiltin(call *ast.CallExpr) bool {
	name, ok := call.Fun.(*ast.Ident)
	return ok && p.symbol(name) == nil && (name.Name == "len" || name.Name == "cap")
}

func ownershipScalar(value ownershipValue) bool {
	return !value.reference && len(value.strings) > 0 && len(value.elements) == 0 && len(value.fields) == 0 && len(value.models) == 0
}

// A captured write discards values, but cannot change an explicitly known
// concrete model type. Interface variables retain no such guarantee.
func (i *ownershipInspector) stableModels(symbol *ownershipSymbol, seen map[*ownershipSymbol]bool) ownershipValue {
	if symbol == nil || seen[symbol] {
		return ownershipValue{}
	}
	seen[symbol] = true
	if symbol.typ != nil {
		return i.modelType(symbol.typ)
	}
	if symbol.valueSpec != nil {
		if symbol.valueSpec.Type != nil {
			return i.modelType(symbol.valueSpec.Type)
		}
		if symbol.valueIndex < len(symbol.valueSpec.Values) {
			return i.stableExpressionModels(symbol.valueSpec.Values[symbol.valueIndex], seen)
		}
		return ownershipValue{}
	}
	return i.stableExpressionModels(symbol.initializer, seen)
}

func (i *ownershipInspector) stableExpressionModels(expression ast.Expr, seen map[*ownershipSymbol]bool) ownershipValue {
	switch expression := expression.(type) {
	case *ast.CompositeLit:
		return i.modelType(expression.Type)
	case *ast.ParenExpr:
		return i.stableExpressionModels(expression.X, seen)
	case *ast.UnaryExpr:
		if expression.Op == token.AND {
			return i.stableExpressionModels(expression.X, seen)
		}
	case *ast.Ident:
		return i.stableModels(i.pkg.symbol(expression), seen)
	case *ast.CallExpr:
		if name, ok := expression.Fun.(*ast.Ident); ok && i.pkg.symbol(name) == nil && len(expression.Args) > 0 {
			if name.Name == "new" || name.Name == "make" {
				return i.modelType(expression.Args[0])
			}
		}
		return i.modelType(expression.Fun)
	}
	return ownershipValue{}
}
