//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

const baseManifest = `apiVersion: workos.app/v1
id: %s
name: %s
version: %s
scope: %s
runtime:
  type: container
  command: ["./serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-bundle
    route: /app
permissions: [artifact.read, agent.task.run]
resources:
  limits:
    memory: 256
health:
  interval: 30
maintainer:
  name: Integration
`

func manifestFor(appID, name, version, scope string) []byte {
	return []byte(fmt.Sprintf(baseManifest, appID, name, version, scope))
}

func appRegistryClients(t *testing.T) appv1connect.AppRegistryServiceClient {
	t.Helper()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := os.Getenv("WORKOS_TEST_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8080"
	}
	return appv1connect.NewAppRegistryServiceClient(httpClient, baseURL)
}

func TestAppRegistryVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	apps := appRegistryClients(t)

	stamp := time.Now().UnixNano()
	boardID := fmt.Sprintf("board-%d", stamp)
	notesID := fmt.Sprintf("notes-%d", stamp)

	// Two semantically different manifests must yield different digests.
	boardDigest := registerApp(t, ctx, apps, boardID, "Integration Board", "1.9.0", "project")
	notesDigest := registerApp(t, ctx, apps, notesID, "Integration Notes", "2.0.0", "user")
	if boardDigest == notesDigest {
		t.Fatal("two different manifests must not share a digest")
	}

	t.Run("ValidateManifestSummarizesAndRejects", func(t *testing.T) {
		valid, err := apps.ValidateManifest(ctx, connect.NewRequest(&appv1.ValidateManifestRequest{
			Yaml: manifestFor(notesID, "Integration Notes", "2.0.0", "user"),
		}))
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if !valid.Msg.GetValid() || valid.Msg.GetNormalized().GetId() != notesID ||
			valid.Msg.GetNormalized().GetManifestDigest() != notesDigest ||
			valid.Msg.GetNormalized().GetScope() != appv1.AppScope_APP_SCOPE_USER {
			t.Fatalf("unexpected validation summary: %#v", valid.Msg)
		}

		// YAML formatting (comments, blank lines, key order) must not change
		// the digest.
		reformattedYAML := "# relocated header comment\n" +
			strings.Replace(string(manifestFor(notesID, "Integration Notes", "2.0.0", "user")), "scope: user\n", "\nscope: user\n", 1)
		reformatted, err := apps.ValidateManifest(ctx, connect.NewRequest(&appv1.ValidateManifestRequest{
			Yaml: []byte(reformattedYAML),
		}))
		if err != nil {
			t.Fatalf("validate reformatted: %v", err)
		}
		if !reformatted.Msg.GetValid() || reformatted.Msg.GetNormalized().GetManifestDigest() != notesDigest {
			t.Fatalf("whitespace-equivalent manifest changed the digest: %#v", reformatted.Msg)
		}

		invalid, err := apps.ValidateManifest(ctx, connect.NewRequest(&appv1.ValidateManifestRequest{
			Yaml: []byte("apiVersion: workos.app/v2\n"),
		}))
		if err != nil {
			t.Fatalf("invalid manifest must be a response, not an error: %v", err)
		}
		if invalid.Msg.GetValid() || len(invalid.Msg.GetViolations()) == 0 || invalid.Msg.GetNormalized() != nil {
			t.Fatalf("unexpected invalid response: %#v", invalid.Msg)
		}
		for _, violation := range invalid.Msg.GetViolations() {
			if strings.Contains(violation, "workos.app/v2") {
				t.Fatalf("violation leaked input value: %q", violation)
			}
		}
	})

	t.Run("IdempotencyAndImmutableVersions", func(t *testing.T) {
		idemID := fmt.Sprintf("idem-%d", stamp)
		key := fmt.Sprintf("replay-%d", stamp)
		first, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: key, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.0", "user"),
		}))
		if err != nil {
			t.Fatalf("first register: %v", err)
		}
		replayed, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: key, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.0", "user"),
		}))
		if err != nil || replayed.Msg.GetApp().GetManifestDigest() != first.Msg.GetApp().GetManifestDigest() {
			t.Fatalf("idempotent replay diverged: %#v err=%v", replayed.Msg, err)
		}

		_, err = apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: key, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.1", "user"),
		}))
		if connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same key different request must be Aborted, got %v", err)
		}

		_, err = apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("other-%d", stamp), ManifestYaml: manifestFor(idemID, "Renamed App", "1.0.0", "user"),
		}))
		if connect.CodeOf(err) != connect.CodeAlreadyExists {
			t.Fatalf("same version different manifest must be AlreadyExists, got %v", err)
		}
	})

	t.Run("SemVerCurrentAndExplicitVersions", func(t *testing.T) {
		registerApp(t, ctx, apps, boardID, "Integration Board", "1.10.0", "project")
		registerApp(t, ctx, apps, boardID, "Integration Board", "1.10.0-rc.3", "project")

		current, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: boardID}))
		if err != nil || current.Msg.GetApp().GetVersion() != "1.10.0" {
			t.Fatalf("current version must follow SemVer precedence: %#v err=%v", current.Msg, err)
		}
		explicit, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: boardID, Version: "1.9.0"}))
		if err != nil || explicit.Msg.GetApp().GetVersion() != "1.9.0" {
			t.Fatalf("explicit version lookup failed: %#v err=%v", explicit.Msg, err)
		}
		_, err = apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: "missing-app"}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown app must be NotFound, got %v", err)
		}
		_, err = apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: boardID, Version: "01.2.3"}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("malformed version must be InvalidArgument, got %v", err)
		}
	})

	t.Run("ListAppsCurrentPerAppOrderedAndPaged", func(t *testing.T) {
		listed, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			Page: &commonv1.PageRequest{PageSize: 100},
		}))
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		ids := map[string]*appv1.WorkOSApp{}
		previous := ""
		for _, app := range listed.Msg.GetApps() {
			if app.GetId() <= previous {
				t.Fatalf("list is not sorted by app id: %v", listed.Msg.GetApps())
			}
			previous = app.GetId()
			ids[app.GetId()] = app
		}
		if board := ids[boardID]; board == nil || board.GetVersion() != "1.10.0" || board.GetManifestDigest() == "" {
			t.Fatalf("board missing or wrong current version: %#v", board)
		}
		if notes := ids[notesID]; notes == nil || notes.GetVersion() != "2.0.0" {
			t.Fatalf("notes missing or wrong current version: %#v", notes)
		}

		// Walk pages of one and collect; must include both registered apps.
		collected := map[string]bool{}
		token := ""
		for {
			page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
				Page: &commonv1.PageRequest{PageSize: 1, PageToken: token},
			}))
			if err != nil {
				t.Fatalf("paged list: %v", err)
			}
			for _, app := range page.Msg.GetApps() {
				collected[app.GetId()] = true
			}
			if page.Msg.GetPage().GetNextPageToken() == "" {
				break
			}
			token = page.Msg.GetPage().GetNextPageToken()
		}
		if !collected[boardID] || !collected[notesID] {
			t.Fatalf("paged walk lost registered apps: %v", collected)
		}
	})

	t.Run("ListAppsProjectContext", func(t *testing.T) {
		httpClient := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
		baseURL := os.Getenv("WORKOS_TEST_URL")
		if baseURL == "" {
			baseURL = "http://127.0.0.1:8080"
		}
		projectClients := projectv1connect.NewProjectServiceClient(httpClient, baseURL)
		key := fmt.Sprintf("app-registry-project-%d", stamp)
		created, err := projectClients.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
			IdempotencyKey: key, Name: "App Registry Context",
		}))
		if err != nil {
			t.Fatalf("create project: %v", err)
		}
		project := created.Msg.GetProject()
		inContext, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 100},
		}))
		if err != nil {
			t.Fatalf("list with project context: %v", err)
		}
		found := 0
		for _, app := range inContext.Msg.GetApps() {
			if app.GetId() == boardID || app.GetId() == notesID {
				found++
			}
		}
		if found != 2 {
			t.Fatalf("project context must list the owner registry catalog, found %d of 2", found)
		}
		if _, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			ProjectId: "00000000-0000-7000-8000-000000000000",
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("unknown project must be NotFound, got %v", err)
		}
		archivedProject, err := projectClients.ArchiveProject(ctx, connect.NewRequest(&projectv1.ArchiveProjectRequest{
			ProjectId: project.GetId(), ExpectedRevision: project.GetRevision(),
		}))
		if err != nil {
			t.Fatalf("archive project: %v", err)
		}
		_ = archivedProject
		if _, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			ProjectId: project.GetId(),
		})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("archived project must be NotFound, got %v", err)
		}
	})

	t.Run("TrustBoundaryFailsClosed", func(t *testing.T) {
		for name, yaml := range map[string][]byte{
			"system scope":     manifestFor(fmt.Sprintf("system-%d", stamp), "System", "1.0.0", "system"),
			"trusted runtime":  []byte(strings.Replace(string(manifestFor(fmt.Sprintf("trusted-%d", stamp), "Trusted", "1.0.0", "user")), "type: container", "type: trusted", 1)),
			"unknown capacity": []byte(strings.Replace(string(manifestFor(fmt.Sprintf("cap-%d", stamp), "Capability", "1.0.0", "user")), "permissions: [artifact.read, agent.task.run]", "permissions: [llm.unlimited]", 1)),
			"secret key name":  []byte(string(manifestFor(fmt.Sprintf("secret-%d", stamp), "Secret", "1.0.0", "user")) + "api_key: not-a-real-value\n"),
		} {
			validated, err := apps.ValidateManifest(ctx, connect.NewRequest(&appv1.ValidateManifestRequest{Yaml: yaml}))
			if err != nil || validated.Msg.GetValid() {
				t.Fatalf("%s must not validate: %#v err=%v", name, validated.Msg, err)
			}
			_, err = apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
				IdempotencyKey: fmt.Sprintf("deny-%s-%d", name, stamp), ManifestYaml: yaml,
			}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("%s must fail closed at registration, got %v", name, err)
			}
		}
	})

	t.Run("ConcurrentRegistrationHasOneWinner", func(t *testing.T) {
		appID := fmt.Sprintf("race-%d", stamp)
		manifestA := manifestFor(appID, "Race A", "3.0.0", "user")
		manifestB := manifestFor(appID, "Race B", "3.0.0", "user")
		results := make(chan error, 2)
		go func() {
			_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "race-a-" + fmt.Sprint(stamp), ManifestYaml: manifestA}))
			results <- err
		}()
		go func() {
			_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "race-b-" + fmt.Sprint(stamp), ManifestYaml: manifestB}))
			results <- err
		}()
		first := <-results
		second := <-results
		if first == nil && second == nil {
			t.Fatal("two different manifests for one version must not both register")
		}
		if first != nil && second != nil {
			t.Fatalf("one registration must win the race: %v / %v", first, second)
		}
		stored, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: appID, Version: "3.0.0"}))
		if err != nil {
			t.Fatalf("get race winner: %v", err)
		}
		winner := stored.Msg.GetApp()
		if winner.GetName() != "Race A" && winner.GetName() != "Race B" {
			t.Fatalf("unexpected winner: %#v", winner)
		}

		// Concurrent identical registrations are all idempotent replays.
		sameManifest := manifestFor(appID, winner.GetName(), "3.1.0", "user")
		var group sync.WaitGroup
		replayErrors := make(chan error, 8)
		for i := 0; i < 8; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
					IdempotencyKey: fmt.Sprintf("race-same-%d-%d", stamp, i), ManifestYaml: sameManifest,
				}))
				replayErrors <- err
			}()
		}
		group.Wait()
		close(replayErrors)
		for err := range replayErrors {
			if err != nil {
				t.Fatalf("concurrent same-digest registration must replay: %v", err)
			}
		}
	})
}

func registerApp(t *testing.T, ctx context.Context, client appv1connect.AppRegistryServiceClient, appID, name, version, scope string) string {
	t.Helper()
	response, err := client.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: fmt.Sprintf("register-%s-%s-%d", appID, version, time.Now().UnixNano()),
		ManifestYaml:   manifestFor(appID, name, version, scope),
	}))
	if err != nil {
		t.Fatalf("register %s@%s: %v", appID, version, err)
	}
	digest := response.Msg.GetApp().GetManifestDigest()
	if !strings.HasPrefix(digest, "sha256:") || response.Msg.GetApp().GetId() != appID || response.Msg.GetApp().GetVersion() != version {
		t.Fatalf("unexpected registration projection: %#v", response.Msg.GetApp())
	}
	return digest
}
