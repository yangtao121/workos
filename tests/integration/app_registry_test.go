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
	"github.com/jackc/pgx/v5"
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
  image: localhost/workos-integration-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-integration-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: [artifact.read, agent.task.run]
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 2
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

// appRegistryDB opens a direct connection to the acceptance database so
// persistence claims (mapping rows, version rows) are verified as facts
// rather than inferred from responses.
func appRegistryDB(t *testing.T) *pgx.Conn {
	t.Helper()
	databaseURL := os.Getenv("WORKOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Skipf("postgres is not reachable for app registry facts: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) }) //nolint:errcheck
	return conn
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	conn := appRegistryDB(t)
	var count int
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows (%s): %v", query, err)
	}
	return count
}

func TestAppRegistryVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	t.Run("IdempotencyMappingIsDurableAndAuthoritative", func(t *testing.T) {
		idemID := fmt.Sprintf("idem-%d", stamp)
		keyFirst := fmt.Sprintf("replay-first-%d", stamp)
		keySecond := fmt.Sprintf("replay-second-%d", stamp)

		first, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: keyFirst, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.0", "user"),
		}))
		if err != nil {
			t.Fatalf("first register: %v", err)
		}

		// A second key over the same immutable fact succeeds — and must be
		// persisted, proven by reusing it for a different request.
		second, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: keySecond, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.0", "user"),
		}))
		if err != nil || second.Msg.GetApp().GetManifestDigest() != first.Msg.GetApp().GetManifestDigest() {
			t.Fatalf("second key must replay the same immutable version: %#v err=%v", second.Msg, err)
		}
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: keySecond, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.1", "user"),
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("persisted second key must conflict on a different request, got %v", err)
		}

		// First-key replay and conflict semantics stay intact.
		replayed, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: keyFirst, ManifestYaml: manifestFor(idemID, "Idempotent App", "1.0.0", "user"),
		}))
		if err != nil || replayed.Msg.GetApp().GetManifestDigest() != first.Msg.GetApp().GetManifestDigest() {
			t.Fatalf("idempotent replay diverged: %#v err=%v", replayed.Msg, err)
		}
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: keyFirst, ManifestYaml: manifestFor(idemID, "Idempotent App", "2.0.0", "user"),
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("same key different request must be Aborted, got %v", err)
		}
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("version-conflict-%d", stamp), ManifestYaml: manifestFor(idemID, "Renamed App", "1.0.0", "user"),
		})); connect.CodeOf(err) != connect.CodeAlreadyExists {
			t.Fatalf("same version different manifest must be AlreadyExists, got %v", err)
		}

		// The database holds exactly one mapping per successful key and one
		// immutable version for the app.
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($1)`,
			[]string{keyFirst, keySecond}); got != 2 {
			t.Fatalf("expected two persisted request mappings, got %d", got)
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_versions WHERE app_id = $1`, idemID); got != 1 {
			t.Fatalf("expected one immutable version, got %d", got)
		}
	})

	t.Run("ConcurrentRegistrationsAgreeOnOneFact", func(t *testing.T) {
		appID := fmt.Sprintf("race-%d", stamp)

		// Eight keys concurrently register the identical manifest: all must
		// succeed against one immutable version, every key must be persisted,
		// and every key must refuse a different request afterwards.
		sameManifest := manifestFor(appID, "Race Same", "3.0.0", "user")
		const concurrency = 8
		keys := make([]string, concurrency)
		for index := range keys {
			keys[index] = fmt.Sprintf("race-same-%d-%d", stamp, index)
		}
		start := make(chan struct{})
		results := make(chan error, concurrency)
		var group sync.WaitGroup
		for _, key := range keys {
			group.Add(1)
			go func(key string) {
				defer group.Done()
				<-start
				_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
					IdempotencyKey: key, ManifestYaml: sameManifest,
				}))
				results <- err
			}(key)
		}
		close(start)
		group.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("concurrent same-digest registration must replay: %v", err)
			}
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($1)`, keys); got != concurrency {
			t.Fatalf("expected %d persisted request mappings, got %d", concurrency, got)
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_versions WHERE app_id = $1 AND version = '3.0.0'`, appID); got != 1 {
			t.Fatalf("expected one immutable version for the manifest, got %d", got)
		}
		for _, key := range keys {
			if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
				IdempotencyKey: key, ManifestYaml: manifestFor(appID, "Race Same", "3.0.1", "user"),
			})); connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("persisted key %s must conflict on a different request, got %v", key, err)
			}
		}

		// One key concurrently registers two different manifests (different
		// versions): exactly one becomes the durable fact, the other is
		// Aborted and leaves neither a version nor a mapping behind.
		sharedKey := fmt.Sprintf("race-shared-%d", stamp)
		leftManifest := manifestFor(appID, "Race Left", "4.0.0", "user")
		rightManifest := manifestFor(appID, "Race Right", "4.1.0", "user")
		keyStart := make(chan struct{})
		keyResults := make(chan error, 2)
		go func() {
			<-keyStart
			_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: sharedKey, ManifestYaml: leftManifest}))
			keyResults <- err
		}()
		go func() {
			<-keyStart
			_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: sharedKey, ManifestYaml: rightManifest}))
			keyResults <- err
		}()
		close(keyStart)
		firstErr, secondErr := <-keyResults, <-keyResults
		if firstErr == nil && secondErr == nil {
			t.Fatal("one of the two requests sharing a key must lose")
		}
		if firstErr != nil && secondErr != nil {
			t.Fatalf("one request sharing a key must win: %v / %v", firstErr, secondErr)
		}
		var loserErr error
		if firstErr != nil {
			loserErr = firstErr
		} else {
			loserErr = secondErr
		}
		if connect.CodeOf(loserErr) != connect.CodeAborted {
			t.Fatalf("the losing shared-key request must be Aborted, got %v / %v", firstErr, secondErr)
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = $1`, sharedKey); got != 1 {
			t.Fatalf("expected exactly one mapping for the shared key, got %d", got)
		}
		// The loser's version row must have been rolled back with its
		// transaction: only the winner's version exists.
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_versions WHERE app_id = $1 AND version IN ('4.0.0','4.1.0')`, appID); got != 1 {
			t.Fatalf("the losing transaction must not leave an orphan version, got %d rows", got)
		}

		// Two keys racing the same version with different digests: one wins,
		// the loser gets AlreadyExists (not Aborted or an internal error),
		// the stored manifest never changes, and the loser's key is not
		// consumed by the failed transaction.
		raceVersion := "5.0.0"
		type digestOutcome struct {
			key    string
			name   string
			digest string
			err    error
		}
		digestSides := []struct {
			key      string
			manifest []byte
			name     string
		}{
			{key: fmt.Sprintf("race-digest-a-%d", stamp), manifest: manifestFor(appID, "Race A", raceVersion, "user"), name: "Race A"},
			{key: fmt.Sprintf("race-digest-b-%d", stamp), manifest: manifestFor(appID, "Race B", raceVersion, "user"), name: "Race B"},
		}
		digestStart := make(chan struct{})
		digestResults := make(chan digestOutcome, len(digestSides))
		for _, side := range digestSides {
			go func(side struct {
				key      string
				manifest []byte
				name     string
			}) {
				<-digestStart
				response, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
					IdempotencyKey: side.key, ManifestYaml: side.manifest}))
				outcome := digestOutcome{key: side.key, name: side.name, err: err}
				if err == nil {
					outcome.digest = response.Msg.GetApp().GetManifestDigest()
				}
				digestResults <- outcome
			}(side)
		}
		close(digestStart)
		firstOutcome, secondOutcome := <-digestResults, <-digestResults
		winners := 0
		for _, outcome := range []digestOutcome{firstOutcome, secondOutcome} {
			if outcome.err == nil {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("exactly one registration must win the digest race: %v / %v", firstOutcome.err, secondOutcome.err)
		}
		var winner, loser digestOutcome
		if firstOutcome.err == nil {
			winner, loser = firstOutcome, secondOutcome
		} else {
			winner, loser = secondOutcome, firstOutcome
		}
		if code := connect.CodeOf(loser.err); code != connect.CodeAlreadyExists {
			t.Fatalf("the losing digest race request must be AlreadyExists, got %v", loser.err)
		}

		// Database facts: only the winner's immutable version and digest
		// exist, the winner's key maps to that version, and the loser's key
		// has no mapping because only successful requests consume keys.
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_versions WHERE app_id = $1 AND version = $2`, appID, raceVersion); got != 1 {
			t.Fatalf("only the winner's immutable version may exist, got %d", got)
		}
		var storedDigest, storedID string
		if err := appRegistryDB(t).QueryRow(context.Background(),
			`SELECT manifest_digest, id FROM workos_core.app_versions WHERE app_id = $1 AND version = $2`,
			appID, raceVersion).Scan(&storedDigest, &storedID); err != nil {
			t.Fatalf("query raced version: %v", err)
		}
		if storedDigest != winner.digest {
			t.Fatalf("stored digest must stay the winner's fact: %s vs %s", storedDigest, winner.digest)
		}
		var mappedID string
		if err := appRegistryDB(t).QueryRow(context.Background(),
			`SELECT app_version_id FROM workos_core.app_registration_requests WHERE idempotency_key = $1`,
			winner.key).Scan(&mappedID); err != nil {
			t.Fatalf("winner key must map to the winner version: %v", err)
		}
		if mappedID != storedID {
			t.Fatalf("winner key mapped to version %s, want the winner version %s", mappedID, storedID)
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = $1`, loser.key); got != 0 {
			t.Fatalf("the failed loser key must not be consumed, found %d mappings", got)
		}

		// The loser's failed transaction must not have burned its key: a
		// fresh, non-conflicting request under that key succeeds, and only
		// then does the key behave as consumed for different requests.
		recovered, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: loser.key, ManifestYaml: manifestFor(appID, "Race Loser Recovery", "5.0.1", "user"),
		}))
		if err != nil || recovered.Msg.GetApp().GetVersion() != "5.0.1" {
			t.Fatalf("loser key must remain usable after AlreadyExists: %#v err=%v", recovered.Msg, err)
		}
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: loser.key, ManifestYaml: manifestFor(appID, "Race Loser Recovery", "5.0.2", "user"),
		})); connect.CodeOf(err) != connect.CodeAborted {
			t.Fatalf("reused loser key must conflict on a different request (Aborted), got %v", err)
		}

		stored, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: appID, Version: raceVersion}))
		if err != nil {
			t.Fatalf("get race winner: %v", err)
		}
		if name := stored.Msg.GetApp().GetName(); name != winner.name {
			t.Fatalf("unexpected winner persisted: %q", name)
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
		// The persistent volume accumulates apps across runs, so walk pages
		// (not one request) and assert global ordering plus both currents.
		ids := map[string]*appv1.WorkOSApp{}
		previous := ""
		token := ""
		pages := 0
		for {
			page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
				Page: &commonv1.PageRequest{PageSize: 100, PageToken: token},
			}))
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			for _, app := range page.Msg.GetApps() {
				if app.GetId() <= previous {
					t.Fatalf("list is not sorted by app id: %s after %s", app.GetId(), previous)
				}
				previous = app.GetId()
				ids[app.GetId()] = app
			}
			pages++
			if page.Msg.GetPage().GetNextPageToken() == "" || (ids[boardID] != nil && ids[notesID] != nil) {
				break
			}
			token = page.Msg.GetPage().GetNextPageToken()
		}
		if board := ids[boardID]; board == nil || board.GetVersion() != "1.10.0" || board.GetManifestDigest() == "" {
			t.Fatalf("board missing or wrong current version: %#v", board)
		}
		if notes := ids[notesID]; notes == nil || notes.GetVersion() != "2.0.0" {
			t.Fatalf("notes missing or wrong current version: %#v", notes)
		}

		// Walk pages of one and collect; must include both registered apps.
		collected := map[string]bool{}
		token = ""
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

	t.Run("ListAppsPagingDefaultsClampAndExactFinalPage", func(t *testing.T) {
		// More than one hundred apps force every paging rule to be real:
		// the default page (50), the clamp (100), and the limit+1 probe.
		// Every registration is recorded and removed again by the subtest
		// cleanup so the shared acceptance volume does not grow per run; the
		// paging assertions never assume an empty database.
		const bulkCount = 105
		bulkPrefix := fmt.Sprintf("bulk-%d-", stamp)
		var fixtureIDs, fixtureKeys []string
		registerFixture := func(appID, key string) {
			fixtureIDs = append(fixtureIDs, appID)
			fixtureKeys = append(fixtureKeys, key)
			if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
				IdempotencyKey: key,
				ManifestYaml:   manifestFor(appID, "Bulk App", "1.0.0", "user"),
			})); err != nil {
				t.Fatalf("register %s: %v", appID, err)
			}
		}
		// Registered before the first fixture row exists so the removal also
		// runs when a later step fails the subtest.
		t.Cleanup(func() { removePagingFixture(t, fixtureKeys, fixtureIDs) })
		for index := 0; index < bulkCount; index++ {
			registerFixture(fmt.Sprintf("%s%03d", bulkPrefix, index), fmt.Sprintf("bulk-%d-%03d", stamp, index))
		}

		// No page block: the default page size is 50 and must be followed by
		// a next token, so records beyond the default are reachable.
		defaultPage, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{}))
		if err != nil {
			t.Fatalf("default list: %v", err)
		}
		if len(defaultPage.Msg.GetApps()) != 50 {
			t.Fatalf("default page must hold exactly 50 apps, got %d", len(defaultPage.Msg.GetApps()))
		}
		if defaultPage.Msg.GetPage().GetNextPageToken() == "" {
			t.Fatal("default page must produce a next token when more apps exist")
		}

		// page_size above the maximum clamps to 100 with a next token.
		overMax, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			Page: &commonv1.PageRequest{PageSize: 101},
		}))
		if err != nil {
			t.Fatalf("over-max list: %v", err)
		}
		if len(overMax.Msg.GetApps()) != 100 {
			t.Fatalf("page size must clamp to 100, got %d", len(overMax.Msg.GetApps()))
		}
		if overMax.Msg.GetPage().GetNextPageToken() == "" {
			t.Fatal("clamped page must produce a next token when more apps exist")
		}

		// Full walk with the clamped page size: no duplicates, no loss,
		// strictly ascending, and every bulk app is reachable.
		seenAll := map[string]bool{}
		previous := ""
		pages := 0
		token := ""
		for {
			page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
				Page: &commonv1.PageRequest{PageSize: 100, PageToken: token},
			}))
			if err != nil {
				t.Fatalf("clamped walk page %d: %v", pages, err)
			}
			for _, app := range page.Msg.GetApps() {
				if seenAll[app.GetId()] {
					t.Fatalf("paged walk repeated app %s", app.GetId())
				}
				if app.GetId() <= previous {
					t.Fatalf("paged walk is not ascending: %s after %s", app.GetId(), previous)
				}
				previous = app.GetId()
				seenAll[app.GetId()] = true
			}
			pages++
			if page.Msg.GetPage().GetNextPageToken() == "" {
				break
			}
			token = page.Msg.GetPage().GetNextPageToken()
		}
		for index := 0; index < bulkCount; index++ {
			appID := fmt.Sprintf("%s%03d", bulkPrefix, index)
			if !seenAll[appID] {
				t.Fatalf("bulk app %s unreachable through paging", appID)
			}
		}

		// An exactly-full final page must not fabricate a token: walk with a
		// page size that divides the total count exactly. The total is the
		// walked size plus the padding needed to reach a multiple of the
		// clamped page size. Sibling tests register fixtures on the shared
		// acceptance volume concurrently, so one bounded re-pad + re-walk
		// converges when a concurrent registration landed in between; the
		// assertion itself never loosens.
		for attempt := 0; attempt < 2; attempt++ {
			walked := len(seenAll)
			padStart := attempt * 100
			if remainder := walked % 100; remainder != 0 {
				for index := 0; index < 100-remainder; index++ {
					registerFixture(fmt.Sprintf("pad-%d-%03d", stamp, padStart+index), fmt.Sprintf("pad-%d-%03d", stamp, padStart+index))
				}
			}
			token, fullPages, lastLen := "", 0, 0
			for {
				page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
					Page: &commonv1.PageRequest{PageSize: 100, PageToken: token},
				}))
				if err != nil {
					t.Fatalf("exact walk page %d: %v", fullPages, err)
				}
				lastLen = len(page.Msg.GetApps())
				fullPages++
				if page.Msg.GetPage().GetNextPageToken() == "" {
					break
				}
				token = page.Msg.GetPage().GetNextPageToken()
			}
			if lastLen == 100 {
				break
			}
			if attempt == 1 {
				t.Fatalf("padding must make the final page exactly full, got %d apps", lastLen)
			}
			// A concurrent registration moved the total: re-derive it from a
			// fresh full walk, then re-pad once.
			token = ""
			seenAll = map[string]bool{}
			for {
				page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
					Page: &commonv1.PageRequest{PageSize: 100, PageToken: token},
				}))
				if err != nil {
					t.Fatalf("recount walk page: %v", err)
				}
				for _, app := range page.Msg.GetApps() {
					seenAll[app.GetId()] = true
				}
				if page.Msg.GetPage().GetNextPageToken() == "" {
					break
				}
				token = page.Msg.GetPage().GetNextPageToken()
			}
		}

		// Request-boundary rules: malformed cursors and identifiers are
		// InvalidArgument, never database errors.
		if _, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			Page: &commonv1.PageRequest{PageSize: 10, PageToken: "not a cursor"},
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("malformed cursor must be InvalidArgument, got %v", err)
		}
		if _, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			ProjectId: "not-a-uuid",
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("malformed project id must be InvalidArgument, got %v", err)
		}
		if _, err := apps.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: "Bad_ID"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("malformed app id must be InvalidArgument, got %v", err)
		}
		if _, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
			Page: &commonv1.PageRequest{PageSize: -1},
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("negative page size must be InvalidArgument, got %v", err)
		}
	})

	t.Run("RequestSizeBoundariesHoldOnTheWire", func(t *testing.T) {
		// One byte over the business limit is a stable InvalidArgument from
		// the application/transport manifest guard.
		oversize := make([]byte, 256*1024+1)
		for index := range oversize {
			oversize[index] = 'x'
		}
		if _, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("oversize-%d", stamp), ManifestYaml: oversize,
		})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("oversize manifest must be InvalidArgument, got %v", err)
		}

		// Far beyond the handler's pre-decode read limit, the request is
		// rejected before the business handler can run.
		huge := make([]byte, 512*1024)
		for index := range huge {
			huge[index] = 'x'
		}
		_, err := apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
			IdempotencyKey: fmt.Sprintf("huge-%d", stamp), ManifestYaml: huge,
		}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("oversized wire request must be ResourceExhausted, got %v", err)
		}
		if strings.Contains(err.Error(), "xxxx") {
			t.Fatalf("wire-limit error must not echo the body: %v", err)
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
		// The owner's catalog has grown beyond one page; walk it inside the
		// project context until both known apps appear or the walk ends.
		found := 0
		token := ""
		for {
			page, err := apps.ListApps(ctx, connect.NewRequest(&appv1.ListAppsRequest{
				ProjectId: project.GetId(), Page: &commonv1.PageRequest{PageSize: 100, PageToken: token},
			}))
			if err != nil {
				t.Fatalf("list with project context: %v", err)
			}
			for _, app := range page.Msg.GetApps() {
				if app.GetId() == boardID || app.GetId() == notesID {
					found++
				}
			}
			if page.Msg.GetPage().GetNextPageToken() == "" || found == 2 {
				break
			}
			token = page.Msg.GetPage().GetNextPageToken()
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
		// The secret key is injected inside the free-form maintainer block of
		// a structurally valid manifest, so only the secret policy can reject.
		secretManifest := strings.Replace(string(manifestFor(fmt.Sprintf("secret-%d", stamp), "Secret", "1.0.0", "user")),
			"maintainer:\n  name: Integration",
			"maintainer:\n  name: Integration\n  api_key: synthetic-not-a-real-value", 1)
		// A synthetic prefixed-token-shaped string used as a mapping key hits
		// the same credential-material policy from the key side.
		credentialKeyManifest := strings.Replace(string(manifestFor(fmt.Sprintf("credkey-%d", stamp), "Credkey", "1.0.0", "user")),
			"maintainer:\n  name: Integration",
			"maintainer:\n  name: Integration\n  \"sk-zzzz0123456789abcdef\": 1", 1)
		deniedApps := []string{
			fmt.Sprintf("secret-%d", stamp),
			fmt.Sprintf("system-%d", stamp),
			fmt.Sprintf("trusted-%d", stamp),
			fmt.Sprintf("cap-%d", stamp),
			fmt.Sprintf("credkey-%d", stamp),
		}
		deniedKeys := []string{}
		for name, yaml := range map[string][]byte{
			"system scope":          manifestFor(fmt.Sprintf("system-%d", stamp), "System", "1.0.0", "system"),
			"trusted runtime":       []byte(strings.Replace(string(manifestFor(fmt.Sprintf("trusted-%d", stamp), "Trusted", "1.0.0", "user")), "type: container", "type: trusted", 1)),
			"unknown capacity":      []byte(strings.Replace(string(manifestFor(fmt.Sprintf("cap-%d", stamp), "Capability", "1.0.0", "user")), "permissions: [artifact.read, agent.task.run]", "permissions: [llm.unlimited]", 1)),
			"secret key name":       []byte(secretManifest),
			"credential-shaped key": []byte(credentialKeyManifest),
		} {
			deniedKeys = append(deniedKeys, fmt.Sprintf("deny-%s-%d", name, stamp))
			validated, err := apps.ValidateManifest(ctx, connect.NewRequest(&appv1.ValidateManifestRequest{Yaml: yaml}))
			if err != nil || validated.Msg.GetValid() {
				t.Fatalf("%s must not validate: %#v err=%v", name, validated.Msg, err)
			}
			if joined := strings.Join(validated.Msg.GetViolations(), " "); strings.Contains(joined, "sk-zzzz") {
				t.Fatalf("%s: violation leaked the synthetic credential-shaped key: %q", name, joined)
			}
			_, err = apps.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
				IdempotencyKey: fmt.Sprintf("deny-%s-%d", name, stamp), ManifestYaml: yaml,
			}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("%s must fail closed at registration, got %v", name, err)
			}
		}
		// A denied manifest must leave no facts behind: no app version and no
		// consumed idempotency key.
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_versions WHERE app_id = ANY($1)`, deniedApps); got != 0 {
			t.Fatalf("denied registrations must not persist app versions, got %d", got)
		}
		if got := countRows(t,
			`SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($1)`, deniedKeys); got != 0 {
			t.Fatalf("denied registrations must not consume idempotency keys, got %d", got)
		}
	})
}

// removePagingFixture deletes exactly the rows one paging run created: first
// the registration-request mappings (the versions they reference are
// RESTRICTed against deletion), then the immutable versions, both selected by
// the run-unique stamp-derived key and app-ID sets. It never touches rows
// outside those sets, and any database failure or surviving row fails the
// test instead of leaking state into the next run.
func removePagingFixture(t *testing.T, keys, appIDs []string) {
	t.Helper()
	if len(keys) == 0 && len(appIDs) == 0 {
		return
	}
	databaseURL := os.Getenv("WORKOS_TEST_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Errorf("cleanup: connect acceptance database: %v", err)
		return
	}
	defer func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Errorf("cleanup: close acceptance connection: %v", err)
		}
	}()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Errorf("cleanup: begin fixture removal: %v", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx,
		`DELETE FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($1)`, keys); err != nil {
		t.Errorf("cleanup: delete fixture request mappings: %v", err)
		return
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM workos_core.app_versions WHERE app_id = ANY($1)`, appIDs); err != nil {
		t.Errorf("cleanup: delete fixture app versions: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("cleanup: commit fixture removal: %v", err)
		return
	}
	var leftover int
	if err := conn.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM workos_core.app_registration_requests WHERE idempotency_key = ANY($1))
		     + (SELECT count(*) FROM workos_core.app_versions WHERE app_id = ANY($2))`,
		keys, appIDs).Scan(&leftover); err != nil {
		t.Errorf("cleanup: verify fixture removal: %v", err)
		return
	}
	if leftover != 0 {
		t.Errorf("cleanup: %d fixture rows survived the removal", leftover)
	}
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
