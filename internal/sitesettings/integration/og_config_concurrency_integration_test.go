//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestSiteSettingOgConfigSectionUpdatesSerializeWithoutLostUpdateIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	service, ctx := newSiteSettingIntegrationService(t, db)

	for iteration := 0; iteration < 5; iteration++ {
		initial := []byte(fmt.Sprintf(`{"home":{"revision":"h0-%d"},"content":{"revision":"c0-%d"}}`, iteration, iteration))
		require.NoError(t, db.Exec(`
			INSERT INTO site_settings (id, default_map_theme_id)
			SELECT 1, id FROM map_theme ORDER BY created_at, id LIMIT 1
			ON CONFLICT (id) DO NOTHING
		`).Error)
		require.NoError(t, db.Model(&model.SiteSettings{}).Where("id = 1").Update("og_image_config", initial).Error)

		start := make(chan struct{})
		errors := make(chan error, 2)
		var wait sync.WaitGroup
		updates := []struct {
			key      string
			revision string
		}{
			{key: "og_image_config.home", revision: fmt.Sprintf("h1-%d", iteration)},
			{key: "og_image_config.content", revision: fmt.Sprintf("c1-%d", iteration)},
		}
		for _, update := range updates {
			update := update
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				value, err := structpb.NewValue(map[string]interface{}{"revision": update.revision})
				if err != nil {
					errors <- err
					return
				}
				_, err = service.SetSetting(ctx, connect.NewRequest(&managev1.SetSettingRequest{
					Key: update.key, Value: value,
				}))
				errors <- err
			}()
		}
		close(start)
		wait.Wait()
		close(errors)
		for err := range errors {
			require.NoError(t, err)
		}

		var settings model.SiteSettings
		require.NoError(t, db.First(&settings, "id = 1").Error)
		var stored map[string]map[string]interface{}
		require.NoError(t, json.Unmarshal(settings.OGImageConfig, &stored))
		require.Equal(t, fmt.Sprintf("h1-%d", iteration), stored["home"]["revision"])
		require.Equal(t, fmt.Sprintf("c1-%d", iteration), stored["content"]["revision"])
	}
}
