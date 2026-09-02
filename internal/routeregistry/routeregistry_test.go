package routeregistry

import (
	"slices"
	"testing"

	"connectrpc.com/connect"
)

func TestPageRouteNamespaceInventory(t *testing.T) {
	t.Parallel()

	fixed := make([]string, 0, len(fixedPageRouteNamespaces))
	for namespace := range fixedPageRouteNamespaces {
		fixed = append(fixed, namespace)
	}
	slices.Sort(fixed)
	if want := []string{
		"_next", "account", "admin", "api", "auth", "category", "changelog", "favicon.ico",
		"files", "login", "manifest.webmanifest", "my", "onboarding", "privacy", "robots.txt",
		"s", "sitemap", "sitemap.xml", "sitemaps", "subscribe", "tag", "terms", "tools",
		"unsubscribe", "user", "verification", "verify",
	}; !slices.Equal(fixed, want) {
		t.Fatalf("fixed Page route namespace drift: got %v, want %v", fixed, want)
	}

}

func TestReservedPageSlugNamespace(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{
		"admin", "ADMIN", "admin/users", "_next/static", "files", "sitemap.xml",
		"manifest.webmanifest", "robots.txt", "verification", "category/news",
		"privacy/history", "tools/transcode", "tools/anything",
	} {
		if !IsReservedPagePath(slug) {
			t.Fatalf("expected %q to be reserved", slug)
		}
	}
	for _, slug := range []string{
		"edit", "EDIT", "about", "", "page-edit", "some/admin", "some/where",
		"page", "pages", "posts", "works", "tools", "TOOLS",
		"forms", "events", "page", "pages", "post", "work", "form",
		"posts/article", "WORKS/example", "series/example", "campaigns/1", "event-series/example",
		"works/something/somewhere", "campaigns/example/more", "series/example/more", "event-series/example/more",
	} {
		if IsReservedPagePath(slug) {
			t.Fatalf("expected %q to be available to Page content", slug)
		}
	}
}

func TestPageResourceRouteInventory(t *testing.T) {
	t.Parallel()

	want := map[string]pageRouteResource{
		"campaigns":    {table: "campaign", hasSlug: false},
		"event-series": {table: "program_event_series", hasSlug: true},
		"events":       {table: "program_event", hasSlug: true},
		"forms":        {table: "form", hasSlug: true},
		"posts":        {table: "post", hasSlug: true},
		"series":       {table: "series", hasSlug: true},
		"works":        {table: "work", hasSlug: true},
	}
	if len(pageRouteResources) != len(want) {
		t.Fatalf("resource route inventory length drift: got %d, want %d", len(pageRouteResources), len(want))
	}
	for namespace, expected := range want {
		if got, ok := pageRouteResources[namespace]; !ok || got != expected {
			t.Fatalf("resource route %q drift: got %#v, want %#v", namespace, got, expected)
		}
	}

	for _, test := range []struct {
		slug      string
		namespace string
		tail      string
		ok        bool
	}{
		{slug: "posts/example", namespace: "posts", tail: "example", ok: true},
		{slug: "WORKS/123", namespace: "works", tail: "123", ok: true},
		{slug: "campaigns/123", namespace: "campaigns", tail: "123", ok: true},
		{slug: "works/example/more", ok: false},
		{slug: "unknown/example", ok: false},
	} {
		_, tail, ok := pageRouteResourceForSlug(test.slug)
		if ok != test.ok || (ok && tail != test.tail) {
			t.Fatalf(
				"pageRouteResourceForSlug(%q) returned tail=%q ok=%v, want tail=%q ok=%v",
				test.slug,
				tail,
				ok,
				test.tail,
				test.ok,
			)
		}
	}
}

func TestPageSlugRoutePathRejectsMalformedPathsAndRouteCollisions(t *testing.T) {
	t.Parallel()

	for _, slug := range []string{
		"", " about", "about ", "/about", "about/", "about//team",
		"about/./team", "about/../team", "admin/team", "tools/transcode",
	} {
		err := ValidatePagePath(slug)
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("expected %q to be rejected as an invalid Page route path, got %v", slug, err)
		}
	}
	validPaths := []string{
		"about",
		"edit",
		"works",
		"posts",
		"tools",
		"some/where",
		"some/admin",
		"works/something/somewhere",
	}
	for _, slug := range validPaths {
		if err := ValidatePagePath(slug); err != nil {
			t.Fatalf("expected %q to be a valid Page route path: %v", slug, err)
		}
	}
}
