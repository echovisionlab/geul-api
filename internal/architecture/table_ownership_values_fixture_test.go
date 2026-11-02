package architecture

import (
	"go/parser"
	"go/token"
	"maps"
	"testing"
)

var ownershipValueFixtures = []struct {
	name, source string
	want         map[string]int
}{
	{
		name: "string const aliases and lexical shadowing",
		source: `const table = "public.post AS p"
func f(db any) { alias := table; db.Table(alias); { table := "member"; db.Table(table) }; db.Table(table) }`,
		want: map[string]int{"post": 2, "member": 1},
	},
	{
		name: "unknown reassignment replaces the previous table",
		source: `func f(db any, table string, definition struct{ RootTable string }) {
db.Table(table); table = "post"; db.Table(table); table = input(); db.Table(table); db.Table(definition.RootTable)
}`,
		want: map[string]int{"post": 1},
	},
	{
		name: "finite local map and positional range registry",
		source: `func f(db any, key string) {
table := map[string]string{"a":"post", "b":"member", "again":"post"}[key]; db.Table(table+" AS owner")
for _, ref := range []struct{table string}{{"post"},{"member"}} { db.Table(ref.table) }
}`,
		want: map[string]int{"post": 2, "member": 2},
	},
	{
		name: "immutable package registry and mutable registry",
		source: `type ref struct{ table string }
var refs = []ref{{table:"post"},{table:"member"}}
var mutable = []ref{{table:"post"}}
func mutate(value []ref) { mutable = value }
func f(db any) { for _, ref := range refs { db.Table(ref.table) }; for _, ref := range mutable { db.Table(ref.table) } }`,
		want: map[string]int{"post": 1, "member": 1},
	},
	{
		name: "model variables pointers slices aliases and conversions",
		source: `type Alias = m.Member
type Different m.Member
func f(db any, input any) {
row := m.Post{}; db.Model(&row)
var member *m.Member; db.Model(member)
var members []*m.Member; members = append(members, &m.Member{}); db.Create(&members)
alias := new(Alias); db.Delete(alias)
converted := m.Member(input); db.Model(converted)
var different Different; db.Model(different)
}`,
		want: map[string]int{"post": 1, "member": 4},
	},
	{
		name: "model field edits retain type and inner variables stay local",
		source: `func f(db any) {
row := m.Post{}; row.Title = "title"; db.Model(row)
{ row := m.Member{}; db.Model(row) }; db.Model(row)
}`,
		want: map[string]int{"post": 2, "member": 1},
	},
	{
		name: "credential model switch discards the returning default",
		source: `func f(db any, kind string) {
var history any
switch kind { case "terms": history = &m.Terms{}; case "privacy": history = &m.Privacy{}; default: return }
db.Model(history)
}`,
		want: map[string]int{"terms_history": 1, "privacy_history": 1},
	},
	{
		name: "unknown model assignment and generic calls remain unknown",
		source: `func f[T any](db any, row any) {
db.Model(row); row = &m.Member{}; row = input(); db.Model(row)
db.Model(new(T)); db.Model(factory()); db.Model(definition.Model())
}`,
		want: map[string]int{},
	},
	{
		name: "a typed model parameter and model collections",
		source: `func f(db any, row *m.Member) {
db.Model(row); db.Create(&[]m.Member{{}})
for _, row := range []any{&m.Member{}, &m.Post{}} { db.Delete(row) }
}`,
		want: map[string]int{"member": 3, "post": 1},
	},
	{
		name: "parameter and initializer scope shadow package names",
		source: `const table="post"
func f(db any, table string){db.Table(table)}
func g(db any){ table:=table; {table:=table;db.Table(table)};db.Table(table) }`,
		want: map[string]int{"post": 2},
	},
	{
		name: "short declarations reuse only the current block",
		source: `func f(db any){
table:="post";table,err:="member",input();_=err;db.Table(table)
if table:="post";condition(){db.Table(table)};db.Table(table)
for table:="post";condition();{db.Table(table);break};db.Table(table)
}`,
		want: map[string]int{"post": 2, "member": 3},
	},
	{
		name:   "range expression precedes its variable scope",
		source: `func f(db any){refs:=[]string{"post"};for _,refs:=range refs{db.Table(refs)}}`,
		want:   map[string]int{"post": 1},
	},
	{
		name:   "switch cases have separate declaration scopes",
		source: `func f(db any,key string){table:="post";switch key{case "a":table:="member";db.Table(table);case "b":db.Table(table)};db.Table(table)}`,
		want:   map[string]int{"post": 2, "member": 1},
	},
	{
		name:   "function literal parameters and captures stay distinct",
		source: `func f(db any){table:="post";callback:=func(table string){db.Table(table)};_=callback;func(){db.Table(table)}();db.Table(table)}`,
		want:   map[string]int{"post": 2},
	},
	{
		name:   "shadowed import and builtins are not model constructors",
		source: `func f(db any){m:=struct{Member func() any}{Member:input};db.Model(m.Member());new:=func(any)any{return input()};db.Model(new(nil))}`,
		want:   map[string]int{},
	},
	{
		name:   "parameter names do not shadow other signature types",
		source: `func f(db any,m int,row *m.Member){db.Model(row)}`,
		want:   map[string]int{"member": 1},
	},
	{
		name:   "generic receiver names shadow package model aliases",
		source: `type T=m.Member;type holder[P any]struct{};func(_ holder[T])f(db any){db.Model(new(T))}`,
		want:   map[string]int{},
	},
	{
		name:   "local type aliases respect block scope",
		source: `type Row=m.Member;func f(db any){db.Model(new(Row));{type Row=m.Post;db.Model(new(Row))};db.Model(new(Row))}`,
		want:   map[string]int{"member": 2, "post": 1},
	},
	{
		name: "escaped package registry remains unknown",
		source: `type ref struct{table string};var refs=[]ref{{table:"post"}}
func mutate(rows []ref){rows[0].table=input()};func init(){mutate(refs)}
func f(db any){for _,r:=range refs{db.Table(r.table)}}`,
		want: map[string]int{},
	},
	{
		name: "forward package aliases preserve escape provenance",
		source: `type ref struct{table string};var first=later;var later=refs;var refs=[]ref{{table:"post"}}
func mutate(rows []ref){rows[0].table=input()};func init(){mutate(first)}
func f(db any){for _,r:=range refs{db.Table(r.table)}}`,
		want: map[string]int{},
	},
	{
		name: "writes through a package registry alias remain unknown",
		source: `type ref struct{table string};var refs=[]ref{{table:"post"}}
func init(){rows:=refs;rows[0].table=input()}
func f(db any){for _,r:=range refs{db.Table(r.table)}}`,
		want: map[string]int{},
	},
	{
		name:   "captured writes invalidate the enclosing value",
		source: `func f(db any){table:="post";func(){table="member"}();db.Table(table)}`,
		want:   map[string]int{},
	},
	{
		name:   "direct models remain visible after a goto target",
		source: `func f(db any){goto done;return;done:db.Model(&m.Member{})}`,
		want:   map[string]int{"member": 1},
	},
	{
		name:   "read-only aliases retain finite registry values",
		source: `type ref struct{table string};var refs=[]ref{{table:"post"}};func f(db any){rows:=refs;db.Table(rows[0].table)}`,
		want:   map[string]int{"post": 1},
	},
	{
		name:   "closure-local writes do not invalidate an outer scalar",
		source: `func f(db any){table:="post";func(){table:="member";_=table}();db.Table(table)}`,
		want:   map[string]int{"post": 1},
	},
	{
		name:   "captured field writes retain concrete model types",
		source: `func f(db any){row:=m.Member{};func(){row.Name=input()}();db.Model(row)}`,
		want:   map[string]int{"member": 1},
	},
	{
		name:   "captured interface assignments do not retain a model type",
		source: `func f(db any){var row any;row=&m.Post{};func(){row=&m.Member{}}();db.Model(row)}`,
		want:   map[string]int{},
	},
}

func TestOwnershipCallsResolveOnlySourceKnownValues(t *testing.T) {
	for _, test := range ownershipValueFixtures {
		t.Run(test.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture\nimport m \""+modulePath+"/internal/model\"\n"+test.source, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			observed := map[string]int{}
			inspectOwnershipCalls([]productionFile{{pkg: "internal/fixture", syntax: file, imports: map[string]string{"m": modulePath + "/internal/model"}}}, map[string]string{
				"Post": "post", "Member": "member", "Terms": "terms_history", "Privacy": "privacy_history",
			}, func(_ string, table string) { observed[table]++ })
			if !maps.Equal(observed, test.want) {
				t.Fatalf("ownership calls = %v, want %v", observed, test.want)
			}
		})
	}
}

func TestOwnershipCallsResolvePackageDeclarationsAcrossFiles(t *testing.T) {
	sources := []string{
		`package fixture; import m "github.com/echovisionlab/geul-api/internal/model"; type Row = m.Member; const rootTable="post"`,
		`package fixture; func f(db any){db.Model(new(Row)); db.Table(rootTable)}`,
	}
	var files []productionFile
	for _, source := range sources {
		syntax, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, productionFile{pkg: "internal/fixture", syntax: syntax, imports: map[string]string{"m": modulePath + "/internal/model"}})
	}
	observed := map[string]int{}
	inspectOwnershipCalls(files, map[string]string{"Member": "member"}, func(_ string, table string) { observed[table]++ })
	if !maps.Equal(observed, map[string]int{"member": 1, "post": 1}) {
		t.Fatalf("ownership calls = %v", observed)
	}
}
