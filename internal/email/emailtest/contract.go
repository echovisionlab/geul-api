package emailtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// Harness is one Store implementation the contract runs against, with the
// rows the Store reads but never creates: templates, layouts, suppressions
// and mail adapters belong to Email Authoring and Email Delivery.
type Harness struct {
	mutationtest.Harness[email.Store]
	// SeedTemplate stores template, whose ID, Key and Name it fills.
	SeedTemplate func(t *testing.T, template model.EmailTemplate) string
	// SeedTemplateContent stores one locale row of a template.
	SeedTemplateContent func(t *testing.T, templateID string, content email.TemplateContent)
	// SeedLayout stores a layout with sourceLocale and returns its ID.
	SeedLayout func(t *testing.T, sourceLocale string) string
	// SeedSuppression stores a suppression of address.
	SeedSuppression func(t *testing.T, address string, released bool)
	// SeedMailAdapter stores adapter, whose ID it fills and returns.
	SeedMailAdapter func(t *testing.T, adapter model.MailAdapter) string
}

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness() Harness {
	store := NewStore()
	return Harness{
		Harness: mutationtest.FakeHarness[email.Store](store),
		SeedTemplate: func(_ *testing.T, template model.EmailTemplate) string {
			template.ID = uuid.NewString()
			store.SeedTemplate(template)
			return template.ID
		},
		SeedTemplateContent: func(_ *testing.T, templateID string, content email.TemplateContent) {
			store.SeedTemplateContent(templateID, content)
		},
		SeedLayout: func(_ *testing.T, sourceLocale string) string {
			id := uuid.NewString()
			store.SeedLayout(id, sourceLocale)
			return id
		},
		SeedSuppression: func(_ *testing.T, address string, released bool) {
			store.SeedSuppression(address, released)
		},
		SeedMailAdapter: func(_ *testing.T, adapter model.MailAdapter) string {
			adapter.ID = uuid.NewString()
			store.SeedMailAdapter(adapter)
			return adapter.ID
		},
	}
}

// RunStoreContract runs every Store contract case against a fresh Harness,
// in parallel.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	for name, run := range storeContract {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run(t, open(t))
		})
	}
}

func transact(t *testing.T, h Harness, work func(ctx context.Context, store email.Store) error) error {
	t.Helper()
	return mutationtest.Transact(t, h.Harness, work)
}

func read(t *testing.T, h Harness, work func(ctx context.Context, store email.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Read(t, h.Harness, work))
}

func text(value string) *string { return &value }

// at is a time both stores keep to the microsecond.
func at(minutes int) time.Time {
	return time.Date(2026, time.November, 1, 12, minutes, 0, 0, time.UTC)
}

func ids[V any](rows []V, id func(V) string) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, id(row))
	}
	return out
}

func localesOf(rows []email.LayoutContent) []string {
	return ids(rows, func(row email.LayoutContent) string { return row.Locale })
}

var storeContract = map[string]func(t *testing.T, h Harness){
	"an active template is found by its event key, an inactive one is not": func(t *testing.T, h Harness) {
		activeKey, inactiveKey := "event-"+uuid.NewString(), "event-"+uuid.NewString()
		active := h.SeedTemplate(t, model.EmailTemplate{IsActive: true, EventKey: &activeKey, SourceLocale: "en"})
		h.SeedTemplate(t, model.EmailTemplate{IsActive: false, EventKey: &inactiveKey, SourceLocale: "en"})

		read(t, h, func(ctx context.Context, store email.Store) error {
			found, err := store.ActiveTemplateByEventKey(ctx, activeKey)
			require.NoError(t, err)
			require.Equal(t, active, found.ID)
			for _, key := range []string{inactiveKey, "event-missing"} {
				_, err = store.ActiveTemplateByEventKey(ctx, key)
				require.ErrorIs(t, err, email.ErrTemplateNotFound, key)
			}
			return nil
		})
	},
	"a template is found by ID whether it is active or not": func(t *testing.T, h Harness) {
		layoutID := h.SeedLayout(t, "en")
		inactive := h.SeedTemplate(t, model.EmailTemplate{IsActive: false, SourceLocale: "ko", LayoutID: &layoutID})

		read(t, h, func(ctx context.Context, store email.Store) error {
			found, err := store.TemplateByID(ctx, inactive)
			require.NoError(t, err)
			require.Equal(t, inactive, found.ID)
			require.Equal(t, &layoutID, found.LayoutID)
			_, err = store.TemplateByID(ctx, uuid.NewString())
			require.ErrorIs(t, err, email.ErrTemplateNotFound)
			return nil
		})
	},
	"a template's source locale and locale rows": func(t *testing.T, h Harness) {
		template := h.SeedTemplate(t, model.EmailTemplate{IsActive: true, SourceLocale: "ko"})
		h.SeedTemplateContent(t, template, email.TemplateContent{Locale: "ko", Subject: text("제목"), ContentHTML: text("<p>본문</p>")})
		h.SeedTemplateContent(t, template, email.TemplateContent{Locale: "en", Subject: text("Subject")})
		h.SeedTemplateContent(t, template, email.TemplateContent{Locale: "fr", Subject: text("Sujet"), ContentHTML: text("<p>Corps</p>")})

		read(t, h, func(ctx context.Context, store email.Store) error {
			locale, found, err := store.TemplateSourceLocale(ctx, template)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "ko", locale)
			_, found, err = store.TemplateSourceLocale(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, found)

			content, err := store.TemplateContent(ctx, template, "ko")
			require.NoError(t, err)
			require.Equal(t, &email.TemplateContent{Locale: "ko", Subject: text("제목"), ContentHTML: text("<p>본문</p>")}, content)
			content, err = store.TemplateContent(ctx, template, "en")
			require.NoError(t, err)
			require.Nil(t, content, "a row without HTML is no content")
			content, err = store.TemplateContent(ctx, template, "ja")
			require.NoError(t, err)
			require.Nil(t, content)

			rows, err := store.TemplateTranslations(ctx, template, []string{"en", "ja"})
			require.NoError(t, err)
			require.Equal(t, []email.TemplateContent{{Locale: "en", Subject: text("Subject")}}, rows)
			return nil
		})
	},
	"a layout's source locale and the locale rows it renders from": func(t *testing.T, h Harness) {
		layout := h.SeedLayout(t, "en")
		other := h.SeedLayout(t, "ko")
		require.NoError(t, transact(t, h, func(ctx context.Context, store email.Store) error {
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "en", text("<main>{{content}}</main>"), text("source"), at(1)))
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "ko", text("<main>ko</main>"), nil, at(2)))
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "fr", nil, text("text only"), at(3)))
			return store.UpsertLayoutEntry(ctx, other, "en", text("<main>other en</main>"), nil, at(4))
		}))

		read(t, h, func(ctx context.Context, store email.Store) error {
			locale, found, err := store.LayoutSourceLocale(ctx, layout)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "en", locale)
			_, found, err = store.LayoutSourceLocale(ctx, uuid.NewString())
			require.NoError(t, err)
			require.False(t, found)

			html, err := store.LayoutContent(ctx, layout, "ko")
			require.NoError(t, err)
			require.Equal(t, text("<main>ko</main>"), html)
			for _, locale := range []string{"fr", "ja"} {
				html, err = store.LayoutContent(ctx, layout, locale)
				require.NoError(t, err)
				require.Nil(t, html, locale)
			}

			rows, err := store.LayoutTranslations(ctx, layout, []string{"ko", "ja"})
			require.NoError(t, err)
			require.Equal(t, []string{"ko"}, localesOf(rows))
			require.Equal(t, text("<main>ko</main>"), rows[0].HTMLContent)

			sources, err := store.SourceLayouts(ctx, []string{layout, other, uuid.NewString()})
			require.NoError(t, err)
			require.ElementsMatch(t, []string{layout}, ids(sources, func(row email.LayoutContent) string { return row.LayoutID }),
				"a layout without a source-locale row has no source")
			require.Equal(t, text("<main>{{content}}</main>"), sources[0].HTMLContent)

			locales, err := store.LayoutLocales(ctx, layout)
			require.NoError(t, err)
			require.Equal(t, []string{"en", "fr", "ko"}, localesOf(locales), "the source locale first, then by locale")
			require.Equal(t, []bool{true, false, false}, []bool{
				locales[0].IsSourceLocale, locales[1].IsSourceLocale, locales[2].IsSourceLocale,
			})
			return nil
		})
	},
	"a layout entry and its targets keep their content and update time": func(t *testing.T, h Harness) {
		layout := h.SeedLayout(t, "ko")
		require.NoError(t, transact(t, h, func(ctx context.Context, store email.Store) error {
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "ko", text("<p>source</p>"), text("source"), at(1)))
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "fr", text("<p>fr</p>"), nil, at(2)))
			return store.UpsertLayoutEntry(ctx, layout, "en", nil, text("en"), at(3))
		}))

		read(t, h, func(ctx context.Context, store email.Store) error {
			entry, err := store.LayoutEntry(ctx, layout, "ko")
			require.NoError(t, err)
			require.Equal(t, &email.LayoutEntry{Locale: "ko", HTMLContent: text("<p>source</p>"), ContentText: text("source"), UpdatedAt: at(1)}, entry)
			entry, err = store.LayoutEntry(ctx, layout, "ja")
			require.NoError(t, err)
			require.Nil(t, entry)

			targets, err := store.LayoutTargets(ctx, layout, "ko")
			require.NoError(t, err)
			require.Equal(t, []email.LayoutEntry{
				{Locale: "en", ContentText: text("en"), UpdatedAt: at(3)},
				{Locale: "fr", HTMLContent: text("<p>fr</p>"), UpdatedAt: at(2)},
			}, targets)
			return nil
		})
	},
	"an upsert replaces the row, an update keeps its time, an insert keeps an existing row": func(t *testing.T, h Harness) {
		layout := h.SeedLayout(t, "en")
		require.NoError(t, transact(t, h, func(ctx context.Context, store email.Store) error {
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "en", text("<p>first</p>"), text("first"), at(1)))
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "en", text("<p>second</p>"), nil, at(2)))
			require.NoError(t, store.UpdateLayoutEntry(ctx, layout, "en", text("<p>third</p>"), text("third")))
			require.NoError(t, store.UpdateLayoutEntry(ctx, layout, "ko", text("<p>absent</p>"), nil))
			require.NoError(t, store.InsertLayoutEntry(ctx, layout, "en", text("<p>ignored</p>"), nil, at(5)))
			return store.InsertLayoutEntry(ctx, layout, "fr", text("<p>fr</p>"), text("fr"), at(6))
		}))

		read(t, h, func(ctx context.Context, store email.Store) error {
			entry, err := store.LayoutEntry(ctx, layout, "en")
			require.NoError(t, err)
			require.Equal(t, &email.LayoutEntry{Locale: "en", HTMLContent: text("<p>third</p>"), ContentText: text("third"), UpdatedAt: at(2)}, entry)
			entry, err = store.LayoutEntry(ctx, layout, "ko")
			require.NoError(t, err)
			require.Nil(t, entry, "an update creates no row")
			entry, err = store.LayoutEntry(ctx, layout, "fr")
			require.NoError(t, err)
			require.Equal(t, &email.LayoutEntry{Locale: "fr", HTMLContent: text("<p>fr</p>"), ContentText: text("fr"), UpdatedAt: at(6)}, entry)
			return nil
		})
	},
	"a failed transaction leaves no layout row": func(t *testing.T, h Harness) {
		layout := h.SeedLayout(t, "en")
		failed := errors.New("caller failed")

		err := transact(t, h, func(ctx context.Context, store email.Store) error {
			require.NoError(t, store.UpsertLayoutEntry(ctx, layout, "en", text("<p>rolled back</p>"), nil, at(1)))
			return failed
		})

		require.ErrorIs(t, err, failed)
		read(t, h, func(ctx context.Context, store email.Store) error {
			entry, err := store.LayoutEntry(ctx, layout, "en")
			require.NoError(t, err)
			require.Nil(t, entry)
			return nil
		})
	},
	"only the unreleased suppressions of the lower-cased address count": func(t *testing.T, h Harness) {
		address := uuid.NewString() + "@example.test"
		released := uuid.NewString() + "@example.test"
		h.SeedSuppression(t, "Bounced-"+address, false)
		h.SeedSuppression(t, "bounced-"+address, false)
		h.SeedSuppression(t, released, true)

		read(t, h, func(ctx context.Context, store email.Store) error {
			for address, want := range map[string]int64{"bounced-" + address: 2, released: 0, "other-" + address: 0} {
				count, err := store.ActiveSuppressionCount(ctx, address)
				require.NoError(t, err)
				require.Equal(t, want, count, address)
			}
			return nil
		})
	},
	"the active mail adapters by priority, then creation": func(t *testing.T, h Harness) {
		var before int64
		read(t, h, func(ctx context.Context, store email.Store) error {
			var err error
			before, err = store.ActiveMailAdapterCount(ctx)
			return err
		})
		config := model.MailAdapterConfig{RawMessage: json.RawMessage(`{}`)}
		adapter := func(name string, active bool, priority, minute int) model.MailAdapter {
			return model.MailAdapter{Name: name, Type: "MAIL_ADAPTER_TYPE_SES", IsActive: active, Priority: priority, Config: config, CreatedAt: at(minute)}
		}
		late := h.SeedMailAdapter(t, adapter("late", true, 1, 2))
		early := h.SeedMailAdapter(t, adapter("early", true, 1, 1))
		first := h.SeedMailAdapter(t, adapter("first", true, -100, 3))
		inactive := h.SeedMailAdapter(t, adapter("inactive", false, -200, 0))

		read(t, h, func(ctx context.Context, store email.Store) error {
			count, err := store.ActiveMailAdapterCount(ctx)
			require.NoError(t, err)
			require.Equal(t, before+3, count)
			adapters, err := store.ActiveMailAdapters(ctx)
			require.NoError(t, err)
			seeded := map[string]bool{late: true, early: true, first: true, inactive: true}
			var order []string
			for _, adapter := range adapters {
				if seeded[adapter.ID] {
					order = append(order, adapter.ID)
				}
			}
			require.Equal(t, []string{first, early, late}, order)
			return nil
		})
	},
}
