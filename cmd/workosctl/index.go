package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
)

// indexAdminClient builds the local IndexAdminService client over the
// indexer's owner-verified Unix admin socket. The socket is the only door to
// these commands; there is no TCP or gateway path by design (ADR-0013 §8).
func indexAdminClient(cfg config.Config) (indexv1connect.IndexAdminServiceClient, error) {
	socket := cfg.Indexer.AdminSocketPath
	if socket == "" {
		return nil, errors.New("WORKOS_INDEX_ADMIN_SOCKET is not configured; the indexer admin socket path is required")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}
	return indexv1connect.NewIndexAdminServiceClient(client, "http://unix"), nil
}

// runIndex executes `workosctl index ...`: status, rebuild, job get/cancel.
func runIndex(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New(indexUsage)
	}
	client, err := indexAdminClient(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		fs := flag.NewFlagSet("index status", flag.ContinueOnError)
		jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		response, err := client.GetIndexAdminStatus(ctx, connect.NewRequest(&indexv1.GetIndexAdminStatusRequest{}))
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(response.Msg)
		}
		printIndexStatus(response.Msg)
		return nil
	case "rebuild":
		fs := flag.NewFlagSet("index rebuild", flag.ContinueOnError)
		all := fs.Bool("all", false, "rebuild every active scope from Core authority")
		owner := fs.String("owner", "", "project owner UUIDv7 (project scope)")
		project := fs.String("project", "", "project UUIDv7 (project scope)")
		key := fs.String("idempotency-key", "", "durable idempotency key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *key == "" || (*all == (*project != "")) {
			return errors.New(indexUsage)
		}
		request := &indexv1.StartIndexRebuildRequest{IdempotencyKey: *key}
		if *all {
			request.Scope = &indexv1.StartIndexRebuildRequest_All{All: &indexv1.StartIndexRebuildRequest_AllScope{}}
		} else {
			request.Scope = &indexv1.StartIndexRebuildRequest_Project{Project: &indexv1.StartIndexRebuildRequest_ProjectScope{
				OwnerUserId: *owner, ProjectId: *project,
			}}
		}
		response, err := client.StartIndexRebuild(ctx, connect.NewRequest(request))
		if err != nil {
			return err
		}
		printIndexJob(response.Msg.GetJob())
		return nil
	case "job":
		if len(args) < 2 {
			return errors.New(indexUsage)
		}
		switch args[1] {
		case "get":
			fs := flag.NewFlagSet("index job get", flag.ContinueOnError)
			jsonOut := fs.Bool("json", false, "emit machine-readable JSON")
			job := fs.String("job", "", "rebuild job UUIDv7")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *job == "" {
				return errors.New(indexUsage)
			}
			response, err := client.GetIndexRebuildJob(ctx, connect.NewRequest(&indexv1.GetIndexRebuildJobRequest{JobId: *job}))
			if err != nil {
				return err
			}
			if *jsonOut {
				return json.NewEncoder(os.Stdout).Encode(response.Msg)
			}
			printIndexJob(response.Msg.GetJob())
			return nil
		case "cancel":
			fs := flag.NewFlagSet("index job cancel", flag.ContinueOnError)
			job := fs.String("job", "", "rebuild job UUIDv7")
			if err := fs.Parse(args[2:]); err != nil {
				return err
			}
			if *job == "" {
				return errors.New(indexUsage)
			}
			response, err := client.CancelIndexRebuildJob(ctx, connect.NewRequest(&indexv1.CancelIndexRebuildJobRequest{JobId: *job}))
			if err != nil {
				return err
			}
			printIndexJob(response.Msg.GetJob())
			return nil
		default:
			return errors.New(indexUsage)
		}
	default:
		return errors.New(indexUsage)
	}
}

const indexUsage = "usage: workosctl index status [--json] | index rebuild --all|--project --idempotency-key <key> | index job get --job <id> [--json] | index job cancel --job <id>"

func printIndexStatus(status *indexv1.GetIndexAdminStatusResponse) {
	out := os.Stdout
	fmt.Fprintf(out, "active generation: %s\n", status.GetActiveGeneration().GetGenerationId())
	if gen := status.GetActiveGeneration(); gen != nil {
		fmt.Fprintf(out, "scope: %s status: %s documents: %d tombstones: %d\n",
			gen.GetScope(), gen.GetStatus(), gen.GetDocumentCount(), gen.GetTombstoneCount())
	}
	fmt.Fprintf(out, "catching up: %t\n", status.GetCatchingUp())
	fmt.Fprintf(out, "pending publications: %d\n", status.GetPendingPublications())
	fmt.Fprintf(out, "indexed through: %s\n", status.GetIndexedThrough())
	fmt.Fprintf(out, "last indexed at: %s\n", status.GetLastIndexedAt())
	if job := status.GetActiveRebuild(); job != nil {
		fmt.Fprintf(out, "active rebuild: %s state: %s scope: %s applied: %d sources: %d\n",
			job.GetJobId(), job.GetState(), job.GetScope(), job.GetAppliedCount(), job.GetSourceCount())
	} else {
		fmt.Fprintf(out, "active rebuild: none\n")
	}
}

func printIndexJob(job *indexv1.IndexAdminRebuildJob) {
	out := os.Stdout
	fmt.Fprintf(out, "job: %s\n", job.GetJobId())
	fmt.Fprintf(out, "scope: %s state: %s\n", job.GetScope(), job.GetState())
	if job.GetOwnerUserId() != "" {
		fmt.Fprintf(out, "owner: %s project: %s\n", job.GetOwnerUserId(), job.GetProjectId())
	}
	fmt.Fprintf(out, "target generation: %s\n", job.GetTargetGenerationId())
	fmt.Fprintf(out, "sources: %d applied: %d tombstones: %d\n",
		job.GetSourceCount(), job.GetAppliedCount(), job.GetTombstoneCount())
	if strings.TrimSpace(job.GetFailureCategory()) != "" {
		fmt.Fprintf(out, "failure: %s\n", job.GetFailureCategory())
	}
	fmt.Fprintf(out, "updated at: %s\n", job.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339Nano))
}
