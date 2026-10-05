//go:build integration

package testutil

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Runs the same API boundary tests against the final image on Docker Desktop.
// Only fixture network addresses and host-only tool paths differ from source mode.
func startBackendImage(t *testing.T, image string, source map[string]string, healthBase string) *runtimeProcess {
	t.Helper()
	env := make(map[string]string, len(source))
	for key, value := range source {
		env[key] = value
	}
	for _, key := range []string{"DATABASE_DSN", "S3_ENDPOINT", "KRATOS_URL", "KRATOS_ADMIN_URL", "EDITOR_COLLAB_URL", "CDN_IMGPROXY_URL", "SPICEDB_ENDPOINT"} {
		value := env[key]
		if key == "SPICEDB_ENDPOINT" {
			value = "tcp://" + value
		}
		address, err := url.Parse(value)
		require.NoError(t, err)
		switch address.Hostname() {
		case "localhost", "127.0.0.1", "::1", "0.0.0.0":
			address.Host = net.JoinHostPort("host.docker.internal", address.Port())
		}
		if key == "SPICEDB_ENDPOINT" {
			env[key] = address.Host
		} else {
			env[key] = address.String()
		}
	}
	for _, key := range []string{"OG_WORKER_SCRIPT", "GLTF_TRANSFORM_PATH", "PARTICLE_MESH_SCRIPT_PATH"} {
		delete(env, key)
	}
	env["TMPDIR"] = "/tmp"
	env["FFMPEG_TEMP_DIR"] = "/tmp/geul-media/transcode"
	env["WAVEFORM_TEMP_DIR"] = "/tmp/geul-media/waveform"
	env["ASSET_OPTIMIZER_TEMP_DIR"] = "/tmp/geul-media/mesh"
	args := []string{"run", "--rm", "--name", "geul-media-test-" + uuid.NewString(), "--add-host", "host.docker.internal:host-gateway"}
	if path := env["WAVEFORM_FFMPEG_PATH"]; path != "" {
		args = append(args, "--mount", "type=bind,src="+filepath.Dir(path)+",dst=/integration-tools,readonly")
		env["WAVEFORM_FFMPEG_PATH"] = "/integration-tools/" + filepath.Base(path)
	}
	for _, key := range []string{"PORT", "MEDIA_DELIVERY_PORT"} {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%s:%s", env[key], env[key]))
	}
	dir := integrationTempDir(t, "image-env")
	envPath := filepath.Join(dir, "runtime.env")
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var contents strings.Builder
	for _, key := range keys {
		require.NotContains(t, env[key], "\n")
		fmt.Fprintf(&contents, "%s=%s\n", key, env[key])
	}
	require.NoError(t, os.WriteFile(envPath, []byte(contents.String()), 0600))
	args = append(args, "--env-file", envPath, image)
	return startRuntimeProcess(t, runtimeProcessSpec{Name: "backend-image", Command: "docker", Args: args, Workdir: appIntegrationRepoPath("../.."), HealthURL: healthBase + "/health"})
}
