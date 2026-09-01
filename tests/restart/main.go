// Command restart is an acceptance helper used by make test-integration to
// prove that completed task state and app registry facts survive process
// restarts.
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	"github.com/yangtao121/workos/gen/go/workos/artifact/v1/artifactv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	manifestvalidator "github.com/yangtao121/workos/internal/core/appregistry/adapters/manifestvalidator"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// usage documents every acceptance-helper subcommand.
const usage = "usage: restart seed | restart verify TASK_ID | restart app-seed | restart app-verify APP_ID_A APP_ID_B | restart install-seed | restart install-verify PROJECT_ID INSTALLATION_ID KEY APP_ID SEED_REVISION | restart surface-seed | restart surface-verify SESSION_URL SESSION_ID PROJECT_ID INSTALLATION_ID KEY | restart bridge-seed | restart bridge-verify TOKEN TASK_ID KEY | restart policy-seed | restart policy-verify TOKEN PROJECT_ID INSTALLATION_ID SURFACE_KEY SET_KEY RUN_KEY TASK_ID | restart grants-seed | restart grants-verify TOKEN PROJECT_ID INSTALLATION_ID SURFACE_KEY SET_KEY SET_PROJECT_REVISION | restart version-seed | restart version-verify PROJECT_ID INSTALLATION_ID TRANSITION_KEY ROLLBACK_KEY TRANSITION_REVISION ROLLBACK_REVISION"

func run() error {
	if len(os.Args) < 2 {
		return errors.New(usage)
	}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	client := &http.Client{Transport: &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := waitReady(ctx, client, baseURL); err != nil {
		return err
	}
	switch os.Args[1] {
	case "seed":
		return seed(ctx, client, baseURL)
	case "verify":
		if len(os.Args) != 3 {
			return errors.New("verify requires a task id")
		}
		return verify(ctx, client, baseURL, os.Args[2])
	case "app-seed":
		return appSeed(ctx, client, baseURL)
	case "app-verify":
		if len(os.Args) != 4 {
			return errors.New("app-verify requires two app ids")
		}
		return appVerify(ctx, client, baseURL, os.Args[2], os.Args[3])
	case "install-seed":
		return installSeed(ctx, client, baseURL)
	case "install-verify":
		return installVerify(ctx, client, baseURL)
	case "surface-seed":
		return surfaceSeed(ctx, client, baseURL)
	case "surface-verify":
		return surfaceVerify(ctx, client, baseURL)
	case "bridge-seed":
		return bridgeSeed(ctx, client, baseURL)
	case "bridge-verify":
		if len(os.Args) != 5 {
			return errors.New("bridge-verify requires TOKEN TASK_ID KEY")
		}
		return bridgeVerify(ctx, client, baseURL, os.Args[2], os.Args[3], os.Args[4])
	case "policy-seed":
		return policySeed(ctx, client, baseURL)
	case "policy-verify":
		if len(os.Args) != 10 {
			return errors.New("policy-verify requires TOKEN PROJECT_ID INSTALLATION_ID SURFACE_KEY SET_KEY RUN_KEY TASK_ID GOAL")
		}
		return policyVerify(ctx, client, baseURL, os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6], os.Args[7], os.Args[8], os.Args[9])
	case "grants-seed":
		return grantsSeed(ctx, client, baseURL)
	case "version-seed":
		return versionSeed(ctx, client, baseURL)
	case "version-verify":
		if len(os.Args) != 8 {
			return errors.New("version-verify requires PROJECT_ID INSTALLATION_ID TRANSITION_KEY ROLLBACK_KEY TRANSITION_REVISION ROLLBACK_REVISION")
		}
		transitionRevision, err := strconv.ParseInt(os.Args[6], 10, 64)
		if err != nil || transitionRevision <= 1 {
			return errors.New("version-verify requires a positive transition revision")
		}
		rollbackRevision, err := strconv.ParseInt(os.Args[7], 10, 64)
		if err != nil || rollbackRevision <= transitionRevision {
			return errors.New("version-verify requires a rollback revision after the transition revision")
		}
		return versionVerify(ctx, client, baseURL, os.Args[2], os.Args[3], os.Args[4], os.Args[5], transitionRevision, rollbackRevision)
	case "grants-verify":
		if len(os.Args) != 8 {
			return errors.New("grants-verify requires TOKEN PROJECT_ID INSTALLATION_ID SURFACE_KEY SET_KEY SET_PROJECT_REVISION")
		}
		setRevision, err := strconv.ParseInt(os.Args[7], 10, 64)
		if err != nil || setRevision <= 1 {
			return errors.New("grants-verify requires TOKEN PROJECT_ID INSTALLATION_ID SURFACE_KEY SET_KEY SET_PROJECT_REVISION")
		}
		return grantsVerify(ctx, client, baseURL, os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6], setRevision)
	default:
		return errors.New(usage)
	}
}

func seed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	key := fmt.Sprintf("restart-project-%d", time.Now().UnixNano())
	providerID := os.Getenv("WORKOS_TEST_PROVIDER")
	if providerID == "" {
		providerID = "fake"
	}
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: key, Name: "Restart Persistence"}))
	if err != nil {
		return fmt.Errorf("create restart project: %w", err)
	}
	activeProject := created.Msg.GetProject()
	if providerID != "fake" {
		bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
			ProjectId: activeProject.GetId(), ExpectedRevision: activeProject.GetRevision(),
			Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: providerID},
		}))
		if err != nil {
			return fmt.Errorf("bind restart project: %w", err)
		}
		activeProject = bound.Msg.GetProject()
		// ADR-0009: a credential-requiring provider carries a server-derived
		// opaque credential_ref; providers without that requirement never do.
		credentialRef := activeProject.GetHarnessBinding().GetCredentialRef()
		if providerID == "deepseek" {
			if len(credentialRef) != 36 {
				return errors.New("server binding did not carry the derived credential reference")
			}
		} else if credentialRef != "" {
			return errors.New("server binding unexpectedly exposed a credential reference")
		}
	}
	response, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
		IdempotencyKey: "task-" + key,
		Input: &agentv1.AgentTaskInput{
			TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: activeProject.GetId()}},
			Role:        "general", Goal: "persist this completed run across service restart",
		},
	}))
	if err != nil {
		return fmt.Errorf("submit restart task: %w", err)
	}
	taskID := response.Msg.GetTask().GetId()
	if response.Msg.GetTask().GetProviderId() != providerID {
		return fmt.Errorf("task provider snapshot mismatch: got %q want %q", response.Msg.GetTask().GetProviderId(), providerID)
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: taskID}))
	if err != nil {
		return fmt.Errorf("watch restart task: %w", err)
	}
	for stream.Receive() {
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("complete restart task: %w", err)
	}
	fmt.Println(taskID)
	return nil
}

func verify(ctx context.Context, client *http.Client, baseURL, taskID string) error {
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	response, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
	if err != nil {
		return fmt.Errorf("get task after restart: %w", err)
	}
	task := response.Msg.GetTask()
	expectedProvider := os.Getenv("WORKOS_TEST_PROVIDER")
	if expectedProvider == "" {
		expectedProvider = "fake"
	}
	if task.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED || task.GetLastEventSequence() < 2 || task.GetProviderId() != expectedProvider {
		return fmt.Errorf("task was not durably completed: state=%s sequence=%d", task.GetState(), task.GetLastEventSequence())
	}
	projectID := task.GetInput().GetTargetScope().GetProjectId()
	projectResponse, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		return fmt.Errorf("get bound project after restart: %w", err)
	}
	if expectedProvider == "fake" {
		if projectResponse.Msg.GetProject().GetHarnessBinding() != nil {
			return errors.New("global-default Project unexpectedly gained a persisted binding")
		}
	} else if binding := projectResponse.Msg.GetProject().GetHarnessBinding(); binding.GetProviderId() != expectedProvider || (expectedProvider == "deepseek") != (len(binding.GetCredentialRef()) == 36) {
		return fmt.Errorf("Project binding was not durably restored: provider=%q", binding.GetProviderId())
	}
	stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{
		TaskId: taskID,
	}))
	if err != nil {
		return fmt.Errorf("resume events after restart: %w", err)
	}
	count := 0
	startedProvider := ""
	for stream.Receive() {
		count++
		if started := stream.Msg().GetEvent().GetRunStarted(); started != nil {
			startedProvider = started.GetProviderId()
		}
	}
	if err := stream.Err(); err != nil {
		return fmt.Errorf("read resumed event after restart: %w", err)
	}
	if int64(count) != task.GetLastEventSequence() || startedProvider != expectedProvider {
		return fmt.Errorf("unexpected restored event stream: count=%d provider=%q", count, startedProvider)
	}
	fmt.Printf("restart persistence verified for task %s\n", taskID)
	return nil
}

// appManifest renders the fixed restart manifest. Digests are re-derived
// through the canonical validator at verify time, proving determinism.
func appManifest(appID, name, version string) []byte {
	return []byte(fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: %s
version: %s
scope: user
runtime:
  type: container
  image: localhost/workos-restart-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-restart-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: [artifact.read]
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 2
maintainer: {}
`, appID, name, version))
}

type restartApp struct {
	id       string
	name     string
	versions []string
	current  string
}

func restartApps(stamp int64) []restartApp {
	return []restartApp{
		{
			id: fmt.Sprintf("restart-alpha-%d", stamp), name: "Restart Alpha",
			versions: []string{"1.0.0", "1.10.0", "1.10.0-rc.7"}, current: "1.10.0",
		},
		{
			id: fmt.Sprintf("restart-beta-%d", stamp), name: "Restart Beta",
			versions: []string{"0.9.0", "1.0.0-rc.5"}, current: "1.0.0-rc.5",
		},
	}
}

func appSeed(ctx context.Context, client *http.Client, baseURL string) error {
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	for _, app := range restartApps(stamp) {
		for _, version := range app.versions {
			if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
				IdempotencyKey: fmt.Sprintf("restart-app-%s-%s", app.id, version),
				ManifestYaml:   appManifest(app.id, app.name, version),
			})); err != nil {
				return fmt.Errorf("register %s@%s: %w", app.id, version, err)
			}
		}
	}
	seeded := restartApps(stamp)
	fmt.Printf("%s %s\n", seeded[0].id, seeded[1].id)
	return nil
}

func appVerify(ctx context.Context, client *http.Client, baseURL, appIDA, appIDB string) error {
	validator, err := manifestvalidator.New()
	if err != nil {
		return fmt.Errorf("load canonical validator: %w", err)
	}
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	var expected []restartApp
	for _, app := range []restartApp{
		{id: appIDA, name: "Restart Alpha", versions: []string{"1.0.0", "1.10.0", "1.10.0-rc.7"}, current: "1.10.0"},
		{id: appIDB, name: "Restart Beta", versions: []string{"0.9.0", "1.0.0-rc.5"}, current: "1.0.0-rc.5"},
	} {
		expected = append(expected, app)
	}

	for _, app := range expected {
		for _, version := range app.versions {
			manifest, violations := validator.Validate(appManifest(app.id, app.name, version))
			if len(violations) > 0 {
				return fmt.Errorf("fixed manifest became invalid: %v", violations)
			}
			stored, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: app.id, Version: version}))
			if err != nil {
				return fmt.Errorf("get %s@%s after restart: %w", app.id, version, err)
			}
			if stored.Msg.GetApp().GetManifestDigest() != manifest.Digest {
				return fmt.Errorf("digest for %s@%s changed across restart", app.id, version)
			}
		}
		current, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: app.id}))
		if err != nil {
			return fmt.Errorf("get current %s after restart: %w", app.id, err)
		}
		if current.Msg.GetApp().GetVersion() != app.current {
			return fmt.Errorf("current version for %s is %s, want %s", app.id, current.Msg.GetApp().GetVersion(), app.current)
		}
	}

	seen := map[string]string{}
	token := ""
	for {
		page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			Page: &commonv1.PageRequest{PageSize: 2, PageToken: token},
		}))
		if err != nil {
			return fmt.Errorf("list apps after restart: %w", err)
		}
		for _, app := range page.Msg.GetApps() {
			seen[app.GetId()] = app.GetVersion()
		}
		if page.Msg.GetPage().GetNextPageToken() == "" {
			break
		}
		token = page.Msg.GetPage().GetNextPageToken()
	}
	for _, app := range expected {
		if seen[app.id] != app.current {
			return fmt.Errorf("list current for %s is %q, want %q", app.id, seen[app.id], app.current)
		}
	}
	fmt.Printf("app registry persistence verified for %s, %s\n", appIDA, appIDB)
	return nil
}

// installSeed registers one app, installs it into a fresh project, prints
// "<project id> <installation id> <install key>" for install-verify.
func installSeed(ctx context.Context, client *http.Client, baseURL string) error {
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("restart-install-%d", stamp)
	registered, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("restart-install-reg-%d", stamp),
		ManifestYaml:   appManifest(appID, "Restart Install", "1.4.2"),
	}))
	if err != nil {
		return fmt.Errorf("register restart install app: %w", err)
	}
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-install-project-%d", stamp), Name: "Restart Installation",
	}))
	if err != nil {
		return fmt.Errorf("create restart install project: %w", err)
	}
	project := created.Msg.GetProject()
	key := fmt.Sprintf("restart-install-key-%d", stamp)
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: key, ProjectId: project.GetId(), AppId: appID, ExpectedProjectRevision: project.GetRevision(),
	}))
	if err != nil {
		return fmt.Errorf("install restart app: %w", err)
	}
	installation := installed.Msg.GetInstallation()
	if installation.GetVersion() != "1.4.2" || installation.GetManifestDigest() != registered.Msg.GetApp().GetManifestDigest() {
		return fmt.Errorf("installation did not pin the registered version: %#v", installation)
	}
	if installed.Msg.GetProjectRevision() != project.GetRevision()+1 {
		return fmt.Errorf("install revision bump missing: %d", installed.Msg.GetProjectRevision())
	}
	fmt.Printf("%s %s %s %s %d\n", project.GetId(), installation.GetId(), key, appID, project.GetRevision())
	return nil
}

// installVerify proves the installation survived a Core restart: the project
// projection still lists the app, the list still returns the pinned instance,
// and the original idempotency key still replays the first result.
func installVerify(ctx context.Context, client *http.Client, baseURL string) error {
	if len(os.Args) != 7 {
		return errors.New("install-verify requires PROJECT_ID INSTALLATION_ID KEY APP_ID SEED_REVISION")
	}
	projectID, installationID, key, appID := os.Args[2], os.Args[3], os.Args[4], os.Args[5]
	seedRevision, err := strconv.ParseInt(os.Args[6], 10, 64)
	if err != nil || seedRevision <= 0 {
		return errors.New("install-verify requires PROJECT_ID INSTALLATION_ID KEY APP_ID SEED_REVISION")
	}
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	listed, err := installations.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: projectID}))
	if err != nil {
		return fmt.Errorf("list installations after restart: %w", err)
	}
	found := ""
	for _, installation := range listed.Msg.GetInstallations() {
		if installation.GetAppId() == appID {
			found = installation.GetId()
		}
	}
	if found != installationID {
		return fmt.Errorf("installation identity changed across restart: listed=%q want %q", found, installationID)
	}
	projectResponse, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		return fmt.Errorf("get project after restart: %w", err)
	}
	ids := projectResponse.Msg.GetProject().GetInstalledAppIds()
	if len(ids) != 1 || ids[0] != appID {
		return fmt.Errorf("installed_app_ids projection lost across restart: %v", ids)
	}
	// The key was consumed by the exact first request (empty version, the
	// project revision at seed time); replaying it with those canonical
	// fields must succeed regardless of the registry's current version.
	replayed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: key, ProjectId: projectID, AppId: appID, ExpectedProjectRevision: seedRevision,
	}))
	if err != nil {
		return fmt.Errorf("replay install key after restart: %w", err)
	}
	if replayed.Msg.GetInstallation().GetId() != installationID {
		return fmt.Errorf("replay changed the installation identity: %q", replayed.Msg.GetInstallation().GetId())
	}
	fmt.Printf("installation persistence verified for %s\n", installationID)
	return nil
}

func waitReady(ctx context.Context, client *http.Client, baseURL string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("gateway did not become ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// surfaceSeed drives the full web bundle chain once and prints
// "<session url> <session id> <project id> <installation id> <create key>"
// for surface-verify.
func surfaceSeed(ctx context.Context, client *http.Client, baseURL string) error {
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	artifacts := artifactv1connect.NewArtifactServiceClient(client, baseURL)
	apps := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)
	stamp := time.Now().UnixNano()
	appID := fmt.Sprintf("restart-surface-%d", stamp)

	created, err := artifacts.CreateArtifact(ctx, connect.NewRequest(&artifactv1.CreateArtifactRequest{
		IdempotencyKey: fmt.Sprintf("restart-surface-artifact-%d", stamp),
		Artifact:       &artifactv1.Artifact{Title: "Restart Surface"},
		WebBundle: &artifactv1.WebBundleContent{
			Entrypoint: "index.html",
			Files: []*artifactv1.WebBundleFile{
				{Path: "index.html", Content: []byte("<!doctype html><title>Restart Surface</title><div id=\"root\"></div><script src=\"app.js\"></script>")},
				{Path: "app.js", Content: []byte("document.getElementById('root').textContent = 'restart-surface-ok';")},
			},
		},
	}))
	if err != nil {
		return fmt.Errorf("create restart artifact: %w", err)
	}
	artifact := created.Msg.GetArtifact()

	manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: Restart Surface App
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: %s
  artifactDigest: %s
surfaces:
  - id: main
    renderer: web-bundle
    route: /
permissions: [artifact.read]
resources: {}
health: {}
maintainer: {}
`, appID, artifact.GetId(), artifact.GetDigest())
	if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("restart-surface-register-%d", stamp), ManifestYaml: []byte(manifest),
	})); err != nil {
		return fmt.Errorf("register restart surface app: %w", err)
	}
	projectResponse, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("restart-surface-project-%d", stamp), Name: "Restart Surface",
	}))
	if err != nil {
		return fmt.Errorf("create restart surface project: %w", err)
	}
	project := projectResponse.Msg.GetProject()
	key := fmt.Sprintf("restart-surface-open-%d", stamp)
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: fmt.Sprintf("restart-surface-install-%d", stamp),
		ProjectId:      project.GetId(), AppId: appID, ExpectedProjectRevision: project.GetRevision(),
	}))
	if err != nil {
		return fmt.Errorf("install restart surface app: %w", err)
	}
	opened, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey:    key,
		AppInstanceId:     installed.Msg.GetInstallation().GetId(),
		ProjectId:         project.GetId(),
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil {
		return fmt.Errorf("open restart surface: %w", err)
	}
	session := opened.Msg.GetSession()
	fmt.Printf("%s %s %s %s %s\n", session.GetUrl(), session.GetId(), project.GetId(), installed.Msg.GetInstallation().GetId(), key)
	return nil
}

// surfaceVerify proves the surface facts survived process restarts: the
// session URL still serves with the security headers, the create key still
// replays the same session, and closing revokes the assets.
func surfaceVerify(ctx context.Context, client *http.Client, baseURL string) error {
	if len(os.Args) != 7 {
		return errors.New("surface-verify requires SESSION_URL SESSION_ID PROJECT_ID INSTALLATION_ID KEY")
	}
	sessionURL, sessionID, projectID, installationID, key := os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)

	response, err := client.Get(baseURL + sessionURL)
	if err != nil {
		return fmt.Errorf("read surface after restart: %w", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("surface asset unavailable after restart: status=%d", response.StatusCode)
	}
	if response.Header.Get("Content-Security-Policy") == "" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		return errors.New("surface security headers lost after restart")
	}

	replayed, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: key, AppInstanceId: installationID, ProjectId: projectID,
		DeviceClass:       surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:          &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
		PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_BUNDLE,
	}))
	if err != nil || replayed.Msg.GetSession().GetId() != sessionID {
		return fmt.Errorf("surface create key did not replay after restart: %v", err)
	}

	if _, err := surfaces.CloseSurface(ctx, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: sessionID})); err != nil {
		return fmt.Errorf("close surface after restart: %w", err)
	}
	closed, err := client.Get(baseURL + sessionURL)
	if err != nil {
		return fmt.Errorf("read closed surface: %w", err)
	}
	closed.Body.Close()
	if closed.StatusCode != http.StatusNotFound {
		return fmt.Errorf("closed surface must fail closed after restart: status=%d", closed.StatusCode)
	}
	fmt.Printf("surface persistence verified for session %s\n", sessionID)
	return nil
}
