//go:build integration
// +build integration

package preflight

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	//"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	//fly "github.com/superfly/fly-go"

	"github.com/superfly/flyctl/test/preflight/testlib"
)

func TestFlyDeployHA(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	if f.SecondaryRegion() == "" {
		t.Skip()
	}

	appName := f.CreateRandomAppName()

	f.Fly(
		"launch --now --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false",
		f.OrgSlug(), appName, f.PrimaryRegion(),
	)
	f.Fly("scale count 1 --region %s --yes", f.SecondaryRegion())

	f.WriteFlyToml(`%s
[mounts]
	source = "data"
	destination = "/data"
	`, f.ReadFile("fly.toml"))

	x := f.FlyAllowExitFailure("deploy")
	require.Contains(f, x.StdErrString(), `needs volumes with name 'data' to fulfill mounts defined in fly.toml`)

	// Create two volumes because fly launch will start 2 machines because of HA setup
	f.Fly("volume create -a %s -r %s -s 1 data -y", appName, f.PrimaryRegion())
	f.Fly("volume create -a %s -r %s -s 1 data -y", appName, f.SecondaryRegion())
	f.Fly("deploy")
}

func TestFlyDeploy_DeployToken_Simple(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false", f.OrgSlug(), appName, f.PrimaryRegion())
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOutString())
	f.Fly("deploy")
}

func TestFlyDeploy_DeployToken_FailingSmokeCheck(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false", f.OrgSlug(), appName, f.PrimaryRegion())
	appConfig := f.ReadFile("fly.toml")
	appConfig += `
[experimental]
  entrypoint = "/bin/false"
`
	f.WriteFlyToml(appConfig)
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOutString())
	deployRes := f.FlyAllowExitFailure("deploy")
	output := deployRes.StdErrString()
	require.Contains(f, output, "the app appears to be crashing")
	require.NotContains(f, output, "401 Unauthorized")
}

func TestFlyDeploy_DeployToken_FailingReleaseCommand(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false", f.OrgSlug(), appName, f.PrimaryRegion())
	appConfig := f.ReadFile("fly.toml")
	appConfig += `
[deploy]
  release_command = "/bin/false"
`
	f.WriteFlyToml(appConfig)
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOut().String())
	deployRes := f.FlyAllowExitFailure("deploy")
	output := deployRes.StdErrString()
	require.Contains(f, output, "exited with non-zero status of 1")
	require.NotContains(f, output, "401 Unauthorized")
}

func TestFlyDeploy_Dockerfile(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()
	f.WriteFile("Dockerfile", `FROM nginx
ENV PREFLIGHT_TEST=true`)
	f.Fly("launch --org %s --name %s --region %s --internal-port 80 --ha=false --now", f.OrgSlug(), appName, f.PrimaryRegion())

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		sshResult := f.Fly("ssh console -C 'printenv PREFLIGHT_TEST'")
		assert.Equal(c, "true", strings.TrimSpace(sshResult.StdOutString()), "expected PREFLIGHT_TEST env var to be set in machine")
	}, 30*time.Second, 2*time.Second)
}

// If this test passes at all, that means that a slow metrics server isn't affecting flyctl
func TestFlyDeploySlowMetrics(t *testing.T) {
	env := make(map[string]string)
	env["FLY_METRICS_BASE_URL"] = "https://flyctl-metrics-slow.fly.dev"
	env["FLY_SEND_METRICS"] = "1"

	f := testlib.NewTestEnvFromEnvWithEnv(t, env)
	appName := f.CreateRandomAppName()

	f.Fly(
		"launch --now --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false",
		f.OrgSlug(), appName, f.PrimaryRegion(),
	)

	f.Fly("deploy")
}

func getRootPath() string {
	_, b, _, _ := runtime.Caller(0)
	return filepath.Dir(b)
}

func copyFixtureIntoWorkDir(workDir, name string, exclusion []string) error {
	src := fmt.Sprintf("%s/fixtures/%s", getRootPath(), name)
	return testlib.CopyDir(src, workDir, exclusion)
}

func TestFlyDeployNodeAppWithRemoteBuilder(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	err := copyFixtureIntoWorkDir(f.WorkDir(), "deploy-node", []string{})
	require.NoError(t, err)

	flyTomlPath := fmt.Sprintf("%s/fly.toml", f.WorkDir())

	appName := f.CreateRandomAppMachines()
	require.NotEmpty(t, appName)

	err = testlib.OverwriteConfig(flyTomlPath, map[string]any{
		"app":    appName,
		"region": f.PrimaryRegion(),
		"env": map[string]string{
			"TEST_ID": f.ID(),
		},
	})
	require.NoError(t, err)

	f.Fly("deploy --remote-only --ha=false")

	f.Fly("deploy --remote-only --strategy immediate --ha=false")

	body, err := testlib.RunHealthCheck(fmt.Sprintf("https://%s.fly.dev", appName))
	require.NoError(t, err)

	require.Contains(t, string(body), fmt.Sprintf("Hello, World! %s", f.ID()))
}

func TestFlyDeployNodeAppWithRemoteBuilderWithoutWireguard(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	// Since this uses a fixture with a size, no need to run it on alternate
	// sizes.
	if f.VMSize != "" {
		t.Skip()
	}

	err := copyFixtureIntoWorkDir(f.WorkDir(), "deploy-node", []string{})
	require.NoError(t, err)

	flyTomlPath := fmt.Sprintf("%s/fly.toml", f.WorkDir())

	appName := f.CreateRandomAppMachines()
	require.NotEmpty(t, appName)

	err = testlib.OverwriteConfig(flyTomlPath, map[string]any{
		"app":    appName,
		"region": f.PrimaryRegion(),
		"env": map[string]string{
			"TEST_ID": f.ID(),
		},
	})
	require.NoError(t, err)

	f.Fly("deploy --remote-only --ha=false --wg=false")

	body, err := testlib.RunHealthCheck(fmt.Sprintf("https://%s.fly.dev", appName))
	require.NoError(t, err)

	require.Contains(t, string(body), fmt.Sprintf("Hello, World! %s", f.ID()))
}

func TestFlyDeployNodeAppWithDepotRemoteBuilder(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	err := copyFixtureIntoWorkDir(f.WorkDir(), "deploy-node", []string{})
	require.NoError(t, err)

	flyTomlPath := fmt.Sprintf("%s/fly.toml", f.WorkDir())

	appName := f.CreateRandomAppMachines()
	require.NotEmpty(t, appName)

	err = testlib.OverwriteConfig(flyTomlPath, map[string]any{
		"app":    appName,
		"region": f.PrimaryRegion(),
		"env": map[string]string{
			"TEST_ID": f.ID(),
		},
	})
	require.NoError(t, err)

	f.Fly("deploy --depot --ha=false")

	f.Fly("deploy --depot --strategy immediate --ha=false")

	body, err := testlib.RunHealthCheck(fmt.Sprintf("https://%s.fly.dev", appName))
	require.NoError(t, err)

	require.Contains(t, string(body), fmt.Sprintf("Hello, World! %s", f.ID()))
}

func TestFlyDeployBasicNodeWithWGEnabled(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	// Since this pins a specific size, we can skip it for alternate VM sizes.
	if f.VMSize != "" {
		t.Skip()
	}

	err := copyFixtureIntoWorkDir(f.WorkDir(), "deploy-node", []string{})
	require.NoError(t, err)

	flyTomlPath := fmt.Sprintf("%s/fly.toml", f.WorkDir())

	appName := f.CreateRandomAppMachines()
	require.NotEmpty(t, appName)

	err = testlib.OverwriteConfig(flyTomlPath, map[string]any{
		"app": appName,
		"env": map[string]string{
			"TEST_ID": f.ID(),
		},
	})
	require.NoError(t, err)

	f.Fly("wireguard websockets enable")

	f.Fly("deploy --remote-only --ha=false")

	f.Fly("wireguard websockets disable")

	body, err := testlib.RunHealthCheck(fmt.Sprintf("https://%s.fly.dev", appName))
	require.NoError(t, err)

	require.Contains(t, string(body), fmt.Sprintf("Hello, World! %s", f.ID()))
}

func TestFlyDeploy_DeployMachinesCheck(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false", f.OrgSlug(), appName, f.PrimaryRegion())
	appConfig := f.ReadFile("fly.toml")
	appConfig += `
		[[http_service.machine_checks]]
            image = "curlimages/curl"
   			entrypoint = ["/bin/sh", "-c"]
			command = ["curl http://[$FLY_TEST_MACHINE_IP]:80"]
		`
	f.WriteFlyToml(appConfig)
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOut().String())
	deployRes := f.Fly("deploy")
	output := deployRes.StdOutString()
	require.Contains(f, output, "Test Machine")
}

func TestFlyDeploy_NoServiceDeployMachinesCheck(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false", f.OrgSlug(), appName, f.PrimaryRegion())
	appConfig := f.ReadFile("fly.toml")
	appConfig += `
		[[machine_checks]]
			image = "curlimages/curl"
			entrypoint = ["/bin/sh", "-c"]
			command = ["curl http://[$FLY_TEST_MACHINE_IP]:80"]
		`
	f.WriteFlyToml(appConfig)
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOut().String())
	deployRes := f.Fly("deploy")
	output := deployRes.StdOutString()
	require.Contains(f, output, "Test Machine")
}

func TestFlyDeploy_DeployMachinesCheckCanary(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false --strategy canary", f.OrgSlug(), appName, f.PrimaryRegion())
	appConfig := f.ReadFile("fly.toml")
	appConfig += `
		[[http_service.machine_checks]]
            image = "curlimages/curl"
   			entrypoint = ["/bin/sh", "-c"]
			command = ["curl http://[$FLY_TEST_MACHINE_IP]:80"]
		`
	f.WriteFlyToml(appConfig)
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOut().String())
	deployRes := f.Fly("deploy")
	output := deployRes.StdOutString()
	require.Contains(f, output, "Test Machine")
}

func TestFlyDeploy_CreateBuilderWDeployToken(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)
	appName := f.CreateRandomAppName()

	f.Fly("launch --org %s --name %s --region %s --image nginx --internal-port 80 --ha=false --strategy canary", f.OrgSlug(), appName, f.PrimaryRegion())
	f.OverrideAuthAccessToken(f.Fly("tokens deploy").StdOutString())
	f.Fly("deploy")
}

func TestDeployManifest(t *testing.T) {
	f := testlib.NewTestEnvFromEnv(t)

	appName := f.CreateRandomAppName()
	f.Fly("launch --org %s --name %s --region %s --image nginx:latest --internal-port 80 --ha=false --strategy rolling", f.OrgSlug(), appName, f.PrimaryRegion())

	var manifestPath = filepath.Join(f.WorkDir(), "manifest.json")

	f.Fly("deploy --export-manifest %s", manifestPath)

	manifest := f.ReadFile("manifest.json")
	require.Contains(t, manifest, `"AppName": "`+appName+`"`)
	require.Contains(t, manifest, `"primary_region": "`+f.PrimaryRegion()+`"`)
	require.Contains(t, manifest, `"internal_port": 80`)
	require.Contains(t, manifest, `"increased_availability": true`)
	// require.Contains(t, manifest, `"strategy": "rolling"`) FIX: fly launch doesn't set strategy
	require.Contains(t, manifest, `"image": "nginx:latest"`)

	deployRes := f.Fly("deploy --from-manifest %s", manifestPath)

	output := deployRes.StdOutString()
	require.Contains(t, output, fmt.Sprintf("Resuming %s deploy from manifest", appName))
}
