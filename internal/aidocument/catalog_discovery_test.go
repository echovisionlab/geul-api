package aidocument

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type catalogDiscoveryPort struct {
	*fakePort
	loadErr  error
	identity DocumentIdentity
	locale   Locale
}

func (p *catalogDiscoveryPort) Load(_ context.Context, identity DocumentIdentity, locale Locale) (Document, error) {
	p.loadCalls++
	p.identity, p.locale = identity, locale
	if p.loadErr != nil {
		return Document{}, p.loadErr
	}
	return p.document, nil
}

func TestDescribeUsesAuthorizedLoadAndCanonicalMetadata(t *testing.T) {
	for _, locale := range []Locale{"ko", "en", "fr"} {
		t.Run(string(locale), func(t *testing.T) {
			document := testDocument(locale, locale != "fr")
			document.Identity.Reference = "44444444-4444-4444-8444-444444444444"
			port := &catalogDiscoveryPort{fakePort: &fakePort{document: document}}
			service, err := NewService(port)
			if err != nil {
				t.Fatal(err)
			}
			request := OpenRequest{Document: document.Identity, Locale: locale}
			result, err := service.Describe(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if port.loadCalls != 1 || port.identity != request.Document || port.locale != locale || port.validateCalls != 0 || len(port.applied) != 0 {
				t.Fatalf("discovery did not use only the owning read boundary: %+v", port)
			}
			metadata, err := service.Open(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Metadata, metadata) || !reflect.DeepEqual(result.Catalog, document.Catalog) {
				t.Fatalf("discovery differs from owning metadata/catalog: %+v", result)
			}
			if _, err := EncodeOpenMetadata(result.Metadata); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDescribeDoesNotExposeCatalogWhenOwningLoadFails(t *testing.T) {
	for _, failure := range []error{errors.New("permission denied"), errors.New("not found"), errors.New("load unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			port := &catalogDiscoveryPort{fakePort: &fakePort{document: testDocument("ko", true)}, loadErr: failure}
			service, err := NewService(port)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Describe(t.Context(), OpenRequest{Document: port.document.Identity, Locale: "ko"})
			if !errors.Is(err, failure) || !reflect.DeepEqual(result, DescribeResult{}) || port.loadCalls != 1 {
				t.Fatalf("owning denial/failure leaked discovery: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestDescribeRejectsInvalidRequestOrLoadedDocument(t *testing.T) {
	for _, name := range []string{"domain", "locale", "identity mismatch", "locale mismatch", "invalid catalog"} {
		t.Run(name, func(t *testing.T) {
			document := testDocument("ko", true)
			request := OpenRequest{Document: document.Identity, Locale: document.Locale}
			wantLoads := 1
			switch name {
			case "domain":
				request.Document.Domain = "unknown"
				wantLoads = 0
			case "locale":
				request.Locale = ""
				wantLoads = 0
			case "identity mismatch":
				document.Identity.Reference = "different"
			case "locale mismatch":
				document.SourceLocale, document.Locale = "en", "en"
			case "invalid catalog":
				document.Catalog.Fields[0].BlockKind = "unknown"
			}
			port := &catalogDiscoveryPort{fakePort: &fakePort{document: document}}
			service, err := NewService(port)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.Describe(t.Context(), request)
			if err == nil || !reflect.DeepEqual(result, DescribeResult{}) || port.loadCalls != wantLoads {
				t.Fatalf("invalid discovery accepted: result=%+v err=%v loads=%d", result, err, port.loadCalls)
			}
		})
	}
}

func TestDescribeOwnsRecursiveCatalogSnapshot(t *testing.T) {
	document := testDocument("en", true)
	text := FieldSchema{Kind: ValueKindText, Ownership: FieldOwnershipSource}
	object := FieldSchema{Kind: ValueKindObject, Ownership: FieldOwnershipSource, Fields: []NestedFieldRule{{Field: "title", Schema: text}}}
	list := FieldSchema{Kind: ValueKindList, Ownership: FieldOwnershipSource, Item: &object, Identity: ListIdentityRule{Kind: ListIdentityFixed, Handles: []RelationItemID{"first"}}}
	document.Catalog.Fields = append(document.Catalog.Fields, FieldRule{BlockKind: "document", Field: "units", ValueKind: ValueKindList, Ownership: FieldOwnershipSource, Schema: &list})
	document.Catalog.RelationFields = append(document.Catalog.RelationFields, RelationFieldRule{BlockKind: "document", Relation: "credits", ItemKind: "credit", Field: "details", ValueKind: ValueKindObject, Ownership: FieldOwnershipSource, Schema: &object})
	port := &catalogDiscoveryPort{fakePort: &fakePort{document: document}}
	service, err := NewService(port)
	if err != nil {
		t.Fatal(err)
	}
	request := OpenRequest{Document: document.Identity, Locale: document.Locale}
	result, err := service.Describe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	result.Catalog.BlockKinds[0] = "changed"
	result.Catalog.Fields[0].Field = "changed"
	result.Catalog.Relations[0].ItemKinds[0] = "changed"
	result.Catalog.Fields[len(result.Catalog.Fields)-1].Schema.Item.Fields[0].Field = "changed"
	result.Catalog.Fields[len(result.Catalog.Fields)-1].Schema.Identity.Handles[0] = "changed"
	result.Catalog.RelationFields[len(result.Catalog.RelationFields)-1].Schema.Fields[0].Field = "changed"
	*result.Metadata.TargetRevision = "changed"
	again, err := service.Describe(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Catalog, document.Catalog) || *again.Metadata.TargetRevision != *document.TargetRevision {
		t.Fatal("caller mutated a port-owned catalog or revision through discovery")
	}
}
