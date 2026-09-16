package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"


	runtimev1 "github.com/yangtao121/workos/gen/go/workos/runtime/v1"
	"github.com/yangtao121/workos/gen/go/workos/runtime/v1/runtimev1connect"
)

// runRuntime serves the runtime-host admin edge (ADR-0033). The import
// command reads a local app-bundle.v1 file and streams its bytes over the
// runtime admin Unix socket; the server re-validates everything (limits,
// paths, deterministic digest) before the bundle becomes ready.
func runRuntime(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: workosctl runtime import-artifact --socket <path> --owner <uuid> --app <app-id> --key <idempotency-key> [--digest sha256:...] --bundle <file>")
	}
	switch args[0] {
	case "import-artifact":
		return runtimeImportArtifact(ctx, args[1:])
	default:
		return errors.New("usage: workosctl runtime import-artifact --socket <path> --owner <uuid> --app <app-id> --key <idempotency-key> [--digest sha256:...] --bundle <file>")
	}
}

func runtimeImportArtifact(ctx context.Context, args []string) error {
	var socket, owner, app, key, digest, bundle string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket":
			i++
			if i < len(args) {
				socket = args[i]
			}
		case "--owner":
			i++
			if i < len(args) {
				owner = args[i]
			}
		case "--app":
			i++
			if i < len(args) {
				app = args[i]
			}
		case "--key":
			i++
			if i < len(args) {
				key = args[i]
			}
		case "--digest":
			i++
			if i < len(args) {
				digest = args[i]
			}
		case "--bundle":
			i++
			if i < len(args) {
				bundle = args[i]
			}
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}
	missing := make([]string, 0, 5)
	for flag, value := range map[string]string{"--socket": socket, "--owner": owner, "--app": app, "--key": key, "--bundle": bundle} {
		if value == "" {
			missing = append(missing, flag)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	file, err := os.Open(bundle)
	if err != nil {
		return fmt.Errorf("open bundle: %w", err)
	}
	defer func() { _ = file.Close() }()

	client := &http.Client{
		Timeout: 10 * time.Minute,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialer := &net.Dialer{Timeout: 3 * time.Second}
				return dialer.DialContext(ctx, "unix", socket)
			},
		},
	}
	admin := runtimev1connect.NewArtifactAdminServiceClient(client, "http://unix")
	stream := admin.ImportArtifact(ctx)
	if err := stream.Send(&runtimev1.ImportArtifactRequest{Part: &runtimev1.ImportArtifactRequest_Start{
		Start: &runtimev1.ImportArtifactStart{
			OwnerUserId: owner, AppId: app, IdempotencyKey: key, ExpectedDigest: digest,
		},
	}}); err != nil {
		return fmt.Errorf("send import metadata: %w", err)
	}
	buffer := make([]byte, 256<<10)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buffer[:n])
			if err := stream.Send(&runtimev1.ImportArtifactRequest{Part: &runtimev1.ImportArtifactRequest_Data{Data: chunk}}); err != nil {
				return fmt.Errorf("stream bundle bytes: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read bundle: %w", readErr)
		}
	}
	response, err := stream.CloseAndReceive()
	if err != nil {
		return fmt.Errorf("import artifact: %w", err)
	}
	result := response.Msg
	fmt.Printf("artifact_id=%s\n", result.GetArtifactId())
	fmt.Printf("digest=%s\n", result.GetDigest())
	fmt.Printf("size_bytes=%d file_count=%d\n", result.GetSizeBytes(), result.GetFileCount())
	fmt.Printf("created=%v origin=%s\n", result.GetCreated(), result.GetOrigin())
	return nil
}
