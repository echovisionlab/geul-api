package emailauthoringtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// Harness is one Email Authoring Store a contract case runs against.
type Harness = mutationtest.Harness[emailauthoring.Store]

// FakeHarness is the Harness of a fresh in-memory Store.
func FakeHarness(*testing.T) Harness {
	store, _ := New()
	return mutationtest.FakeHarness[emailauthoring.Store](store)
}

// RunStoreContract runs every Store contract case against a fresh Harness.
func RunStoreContract(t *testing.T, open func(t *testing.T) Harness) {
	t.Helper()
	mutationtest.RunContract(t, open, storeContract)
}

// at is a time the database stores without losing precision.
func at() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

func transact(t *testing.T, h Harness, work func(ctx context.Context, store emailauthoring.Store) error) {
	t.Helper()
	require.NoError(t, mutationtest.Transact(t, h, work))
}

// insertTemplate stores a template with its content document and the
// source locale row of subject.
func insertTemplate(t *testing.T, h Harness, key string) emailauthoring.Template {
	t.Helper()
	var template emailauthoring.Template
	transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
		document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: emailcontent.Profile, SourceLocale: "en"})
		if err != nil {
			return err
		}
		documentID := document.Document.ID.String()
		now := at()
		template = emailauthoring.Template{
			Key: key, Name: "Name " + key, SourceLocale: "en", IsActive: true,
			ContentDocumentID: &documentID, CreatedAt: now, UpdatedAt: &now,
		}
		if err := store.InsertTemplate(ctx, &template); err != nil {
			return err
		}
		subject := "Subject " + key
		_, err = store.InsertLocale(ctx, template.ID, "en", &subject, now)
		return err
	})
	return template
}

// insertLayout stores a layout with its content document and source HTML.
func insertLayout(t *testing.T, h Harness, key string) emailauthoring.Layout {
	t.Helper()
	var layout emailauthoring.Layout
	transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
		document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: "compact", SourceLocale: "en"})
		if err != nil {
			return err
		}
		now := at()
		layout = emailauthoring.Layout{
			Key: key, Name: "Layout " + key, SourceLocale: "en",
			ContentDocumentID: document.Document.ID.String(), CreatedAt: now, UpdatedAt: now,
		}
		if err := store.InsertLayout(ctx, &layout); err != nil {
			return err
		}
		html, text := LayoutSourceHTML, "Hello"
		return store.SaveLayoutSource(ctx, layout.ID, "en", email.LayoutTranslationDocument{ContentHTML: &html, ContentText: &text}, now)
	})
	return layout
}

// LayoutSourceHTML is canonical layout source HTML with one text unit.
const LayoutSourceHTML = "<p><!--geul-unit:text:0d55f289-ff23-43fe-8547-a85109cdd449-->Hello</p>{{content}}"

func findTemplate(t *testing.T, h Harness, id string) (emailauthoring.Template, error) {
	t.Helper()
	var template emailauthoring.Template
	err := mutationtest.Read(t, h, func(ctx context.Context, store emailauthoring.Store) (err error) {
		template, err = store.FindTemplate(ctx, id)
		return err
	})
	return template, err
}

func key() string { return "key_" + uuid.NewString()[:8] }

var errRollback = errors.New("roll back")

var storeContract = map[string]func(t *testing.T, h Harness){
	"a template key another template holds is taken": func(t *testing.T, h Harness) {
		taken := insertTemplate(t, h, key())
		err := mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: emailcontent.Profile, SourceLocale: "en"})
			if err != nil {
				return err
			}
			documentID, now := document.Document.ID.String(), at()
			return store.InsertTemplate(ctx, &emailauthoring.Template{
				Key: taken.Key, Name: "Other", SourceLocale: "en", ContentDocumentID: &documentID, CreatedAt: now, UpdatedAt: &now,
			})
		})
		require.ErrorIs(t, err, emailauthoring.ErrTemplateKeyTaken)
	},
	"a missing template is not found by every read": func(t *testing.T, h Harness) {
		missing := uuid.NewString()
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			_, err := store.FindTemplate(ctx, missing)
			require.ErrorIs(t, err, emailauthoring.ErrTemplateNotFound)
			_, err = store.TemplateLayoutID(ctx, missing)
			require.ErrorIs(t, err, emailauthoring.ErrTemplateNotFound)
			_, err = store.TemplateDocument(ctx, missing, true)
			require.ErrorIs(t, err, emailauthoring.ErrTemplateNotFound)
			_, err = store.TemplateSourceLocale(ctx, missing, true)
			require.ErrorIs(t, err, emailauthoring.ErrTemplateNotFound)
			_, err = store.LockTemplate(ctx, missing)
			require.ErrorIs(t, err, emailauthoring.ErrTemplateNotFound)
			require.ErrorIs(t, store.LockTranslationSource(ctx, "email_template", missing), emailauthoring.ErrTemplateNotFound)
			require.ErrorIs(t, store.LockTranslationSource(ctx, "email_layout", missing), emailauthoring.ErrLayoutNotFound)
			return nil
		}))
	},
	"an inserted template reads back with its document and source locale": func(t *testing.T, h Harness) {
		inserted := insertTemplate(t, h, key())
		require.NotEmpty(t, inserted.ID)
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			documentID, err := store.TemplateDocument(ctx, inserted.ID, false)
			require.NoError(t, err)
			require.Equal(t, inserted.ContentDocumentID, documentID)
			sourceLocale, err := store.TemplateSourceLocale(ctx, inserted.ID, false)
			require.NoError(t, err)
			require.Equal(t, "en", sourceLocale)
			row, exists, err := store.LocaleRow(ctx, inserted.ID, "en", emailcontent.Unlocked)
			require.NoError(t, err)
			require.True(t, exists)
			require.Equal(t, "Subject "+inserted.Key, *row.Subject)
			return nil
		}))
	},
	"an update writes the changed fields and clears the layout": func(t *testing.T, h Harness) {
		layout := insertLayout(t, h, key())
		template := insertTemplate(t, h, key())
		now := at()
		name, active := "Renamed", false
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			return store.UpdateTemplate(ctx, template.ID, emailauthoring.TemplateChange{SetLayout: true, LayoutID: &layout.ID, UpdatedAt: now})
		})
		stored, err := findTemplate(t, h, template.ID)
		require.NoError(t, err)
		require.Equal(t, layout.ID, *stored.LayoutID)
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			return store.UpdateTemplate(ctx, template.ID, emailauthoring.TemplateChange{Name: &name, IsActive: &active, SetLayout: true, UpdatedAt: now})
		})
		stored, err = findTemplate(t, h, template.ID)
		require.NoError(t, err)
		require.Equal(t, "Renamed", stored.Name)
		require.False(t, stored.IsActive)
		require.Nil(t, stored.LayoutID)
		require.True(t, now.Equal(*stored.UpdatedAt))
	},
	"custom variables leave a system template alone": func(t *testing.T, h Harness) {
		custom := insertTemplate(t, h, key())
		var system emailauthoring.Template
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: emailcontent.Profile, SourceLocale: "en"})
			if err != nil {
				return err
			}
			documentID, now := document.Document.ID.String(), at()
			system = emailauthoring.Template{Key: key(), Name: "System", SourceLocale: "en", IsSystem: true, ContentDocumentID: &documentID, CreatedAt: now, UpdatedAt: &now}
			return store.InsertTemplate(ctx, &system)
		})
		variables := []emailauthoring.TemplateVariable{{Name: "first"}, {Name: "second"}}
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			if err := store.SetCustomTemplateVariables(ctx, custom.ID, variables); err != nil {
				return err
			}
			return store.SetCustomTemplateVariables(ctx, system.ID, variables)
		})
		stored, err := findTemplate(t, h, custom.ID)
		require.NoError(t, err)
		require.Equal(t, []string{"first", "second"}, []string{stored.Variables[0].Name, stored.Variables[1].Name})
		stored, err = findTemplate(t, h, system.ID)
		require.NoError(t, err)
		require.Empty(t, stored.Variables)
	},
	"an event mapping moves to the requested template": func(t *testing.T, h Harness) {
		first, second := insertTemplate(t, h, key()), insertTemplate(t, h, key())
		event, now := "welcome_"+uuid.NewString()[:6], at()
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			return store.SetEventMapping(ctx, first.ID, event, now)
		})
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			locked, err := store.LockEventMappingTemplates(ctx, event, second.ID)
			require.NoError(t, err)
			require.Len(t, locked, 2)
			require.Less(t, locked[0].ID, locked[1].ID)
			if err := store.ClearEventMapping(ctx, event, now); err != nil {
				return err
			}
			return store.SetEventMapping(ctx, second.ID, event, now)
		})
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			mapped, err := store.MappedTemplates(ctx)
			require.NoError(t, err)
			var mappedIDs []string
			for _, template := range mapped {
				if *template.EventKey == event {
					mappedIDs = append(mappedIDs, template.ID)
				}
			}
			require.Equal(t, []string{second.ID}, mappedIDs)
			return nil
		}))
	},
	"a locked template makes a second writer wait for the first commit": func(t *testing.T, h Harness) {
		template := insertTemplate(t, h, key())
		name := "First"
		err := mutationtest.RequireSecondWaits(t, h,
			func(ctx context.Context, store emailauthoring.Store) error {
				_, err := store.LockTemplate(ctx, template.ID)
				return err
			},
			func(ctx context.Context, store emailauthoring.Store) error {
				return store.UpdateTemplate(ctx, template.ID, emailauthoring.TemplateChange{Name: &name, UpdatedAt: at()})
			},
			func(ctx context.Context, store emailauthoring.Store) error {
				locked, err := store.LockTemplate(ctx, template.ID)
				if err == nil && locked.Name != "First" {
					return errors.New("the second writer did not see the first commit")
				}
				return err
			},
		)
		require.NoError(t, err)
	},
	"a failed transaction leaves no template": func(t *testing.T, h Harness) {
		var id string
		err := mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: emailcontent.Profile, SourceLocale: "en"})
			if err != nil {
				return err
			}
			documentID, now := document.Document.ID.String(), at()
			template := emailauthoring.Template{Key: key(), Name: "Gone", SourceLocale: "en", ContentDocumentID: &documentID, CreatedAt: now, UpdatedAt: &now}
			if err := store.InsertTemplate(ctx, &template); err != nil {
				return err
			}
			id = template.ID
			return errRollback
		})
		require.ErrorIs(t, err, errRollback)
		_, err = findTemplate(t, h, id)
		require.ErrorIs(t, err, emailauthoring.ErrTemplateNotFound)
	},
	"locale rows insert, update, project, upsert and delete": func(t *testing.T, h Harness) {
		template := insertTemplate(t, h, key())
		now := at()
		later := now.Add(time.Second)
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			subject := "Japanese"
			created, err := store.InsertLocale(ctx, template.ID, "ja", &subject, now)
			require.NoError(t, err)
			require.True(t, created)
			updated, err := store.UpdateLocale(ctx, template.ID, "ja", nil, later)
			require.NoError(t, err)
			require.True(t, updated)
			missing, err := store.UpdateLocale(ctx, template.ID, "fr", nil, later)
			require.NoError(t, err)
			require.False(t, missing)
			projected, err := store.ProjectLocale(ctx, template.ID, "ja", emailcontent.LocaleProjection{HTML: "<p>x</p>", Text: "x"})
			require.NoError(t, err)
			require.True(t, projected)
			replaced := "Replaced"
			return store.UpsertLocaleSubject(ctx, template.ID, "ko", &replaced, now)
		})
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			names, err := store.LocaleNames(ctx, template.ID)
			require.NoError(t, err)
			require.Equal(t, []string{"en", "ja", "ko"}, names)
			row, exists, err := store.LocaleRow(ctx, template.ID, "ja", emailcontent.ShareLock)
			require.NoError(t, err)
			require.True(t, exists)
			require.Nil(t, row.Subject)
			require.True(t, later.Equal(row.UpdatedAt))
			return nil
		}))
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			deleted, err := store.DeleteLocale(ctx, template.ID, "ja")
			require.NoError(t, err)
			require.True(t, deleted)
			deleted, err = store.DeleteLocale(ctx, template.ID, "ja")
			require.NoError(t, err)
			require.False(t, deleted)
			return nil
		})
	},
	"touching an owner moves its updated_at": func(t *testing.T, h Harness) {
		template := insertTemplate(t, h, key())
		touched := at().Add(time.Hour)
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			return store.TouchOwner(ctx, template.ID, touched)
		})
		stored, err := findTemplate(t, h, template.ID)
		require.NoError(t, err)
		require.True(t, touched.Equal(*stored.UpdatedAt))
	},
	"a stale expected revision cannot advance the document": func(t *testing.T, h Harness) {
		template := insertTemplate(t, h, key())
		documentID := uuid.MustParse(*template.ContentDocumentID)
		fence := func(context.Context, uuid.UUID) (contentblock.DomainContext, error) {
			return contentblock.DomainContext{SourceLocale: "en"}, nil
		}
		changed := func(context.Context) (contentblock.MetadataEffect, error) {
			return contentblock.MetadataEffect{Changed: true}, nil
		}
		var first contentblock.AdvanceResult
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			snapshot, err := store.LockedContentSnapshot(ctx, documentID, "en")
			if err != nil {
				return err
			}
			first, err = store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: snapshot.Document.Revision}, fence, changed)
			return err
		})
		require.True(t, first.Changed)
		err := mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			_, err := store.AdvanceContentRevision(ctx, contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: uuid.New()}, fence, changed)
			return err
		})
		var stale *contentblock.StaleRevisionError
		require.ErrorAs(t, err, &stale)
		require.Equal(t, first.DocumentRevision, stale.CurrentRevision)
	},
	"a layout key another layout holds is taken on insert and update": func(t *testing.T, h Harness) {
		taken, other := insertLayout(t, h, key()), insertLayout(t, h, key())
		err := mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			return store.UpdateLayout(ctx, other.ID, emailauthoring.LayoutChange{Key: &taken.Key, UpdatedAt: at()})
		})
		require.ErrorIs(t, err, emailauthoring.ErrLayoutKeyTaken)
		err = mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			document, err := store.CreateContentDocument(ctx, contentblock.CreateInput{Profile: "compact", SourceLocale: "en"})
			if err != nil {
				return err
			}
			now := at()
			return store.InsertLayout(ctx, &emailauthoring.Layout{
				Key: taken.Key, Name: "Other", SourceLocale: "en", ContentDocumentID: document.Document.ID.String(), CreatedAt: now, UpdatedAt: now,
			})
		})
		require.ErrorIs(t, err, emailauthoring.ErrLayoutKeyTaken)
	},
	"layouts lock in ID order and count their templates": func(t *testing.T, h Harness) {
		first, second := insertLayout(t, h, key()), insertLayout(t, h, key())
		template := insertTemplate(t, h, key())
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			return store.UpdateTemplate(ctx, template.ID, emailauthoring.TemplateChange{SetLayout: true, LayoutID: &second.ID, UpdatedAt: at()})
		})
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			ids := []string{first.ID, second.ID, uuid.NewString()}
			locked, err := store.LockLayouts(ctx, ids)
			require.NoError(t, err)
			require.ElementsMatch(t, []string{first.ID, second.ID}, locked)
			require.Less(t, locked[0], locked[1])
			counts, err := store.TemplateCounts(ctx, []string{first.ID, second.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]int32{second.ID: 1}, counts)
			return nil
		})
	},
	"a layout authority carries its document revision": func(t *testing.T, h Harness) {
		layout := insertLayout(t, h, key())
		require.NoError(t, mutationtest.Transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			authority, err := store.LayoutAuthority(ctx, layout.ID, emailcontent.UpdateLock)
			require.NoError(t, err)
			require.Equal(t, layout.ContentDocumentID, authority.DocumentID.String())
			require.Equal(t, "en", authority.SourceLocale)
			snapshot, err := store.LockedContentSnapshot(ctx, authority.DocumentID, "en")
			require.NoError(t, err)
			require.Equal(t, snapshot.Document.Revision, authority.DocumentRevision)
			_, err = store.LayoutAuthority(ctx, uuid.NewString(), emailcontent.ShareLock)
			require.ErrorIs(t, err, emailauthoring.ErrLayoutNotFound)
			return nil
		}))
	},
	"layout translations save, seed, read and delete": func(t *testing.T, h Harness) {
		layout := insertLayout(t, h, key())
		now := at()
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			sourceLocale, found, err := store.LayoutSourceLocale(ctx, layout.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "en", sourceLocale)
			if err := store.SeedLayoutTranslation(ctx, layout.ID, "ja", "en", now); err != nil {
				return err
			}
			html, text := "<p>ja</p>{{content}}", "ja"
			return store.UpsertLayoutTranslation(ctx, layout.ID, "ko", translation.EntryWrite{ContentHTML: &html, ContentText: &text, Now: now})
		})
		transact(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			seeded, err := store.LockLayoutTranslationEntry(ctx, layout.ID, "ja", emailcontent.UpdateLock)
			require.NoError(t, err)
			require.NotNil(t, seeded)
			require.NotNil(t, seeded.ContentHTML)
			upserted, err := store.LayoutTranslationEntry(ctx, layout.ID, "ko")
			require.NoError(t, err)
			require.Equal(t, "<p>ja</p>{{content}}", *upserted.ContentHTML)
			sources, err := store.CanonicalLayoutSources(ctx, []string{layout.ID})
			require.NoError(t, err)
			require.Equal(t, map[string]string{layout.ID: LayoutSourceHTML}, sources)
			deleted, err := store.DeleteLayoutTranslation(ctx, layout.ID, "ja")
			require.NoError(t, err)
			require.True(t, deleted)
			missing, err := store.LayoutTranslationEntry(ctx, layout.ID, "ja")
			require.NoError(t, err)
			require.Nil(t, missing)
			return nil
		})
	},
	"a list pages and counts every layout": func(t *testing.T, h Harness) {
		var before int64
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store emailauthoring.Store) (err error) {
			_, before, err = store.ListLayouts(ctx, emailauthoring.ListQuery{Page: queryutil.Pagination{Limit: 1}})
			return err
		}))
		for range 3 {
			insertLayout(t, h, key())
		}
		require.NoError(t, mutationtest.Read(t, h, func(ctx context.Context, store emailauthoring.Store) error {
			layouts, total, err := store.ListLayouts(ctx, emailauthoring.ListQuery{Page: queryutil.Pagination{Limit: 2}})
			require.NoError(t, err)
			require.Equal(t, before+3, total)
			require.Len(t, layouts, 2)
			return nil
		}))
	},
}
