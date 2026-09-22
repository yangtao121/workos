package application

import (
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
	"testing"
)

func TestAttachOnlyNeverEnsuresAndValidatesExactInstallation(t *testing.T) {
	for _, change := range []string{"none", "owner", "project", "instance", "version", "generation", "stopped"} {
		t.Run(change, func(t *testing.T) {
			repository := newFakeRepository()
			resolved := containerResolution()
			resolver := &fakeResolver{resolved: resolved}
			command := validCommand("attach-only")
			command.AttachOnly = true
			command.PreferredRenderer = ""
			command.ExpectedWorkloadID = "0198d7ea-2110-7c42-b659-c5e4d73bc390"
			command.ExpectedWorkloadGeneration = 4
			handle := ports.WorkloadHandle{ID: command.ExpectedWorkloadID, Generation: 4, OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID, AppInstanceID: command.AppInstanceID, AppID: resolved.AppID, AppVersion: resolved.Version, ManifestDigest: resolved.ManifestDigest, LifecycleMode: 2, Endpoint: "127.0.0.1:41000"}
			workloads := &fakeWorkloads{lookupHandle: handle}
			switch change {
			case "owner":
				workloads.lookupHandle.OwnerUserID = "foreign"
			case "project":
				workloads.lookupHandle.ProjectID = "foreign"
			case "instance":
				workloads.lookupHandle.AppInstanceID = "foreign"
			case "version":
				workloads.lookupHandle.AppVersion = "stale"
			case "generation":
				workloads.lookupHandle.Generation = 5
			case "stopped":
				workloads.lookupErr = ports.ErrWorkloadUnsupported
			}
			service := newTestServiceWithWorkloads(repository, resolver, workloads)
			created, err := service.Create(context.Background(), command)
			if workloads.ensureCalls != 0 {
				t.Fatal("restoration started a program")
			}
			if change != "none" {
				if !errors.Is(err, domain.ErrWorkloadNotRunning) {
					t.Fatalf("invalid identity accepted: %v", err)
				}
				if len(repository.sessions) != 0 {
					t.Fatal("failed attachment consumed session")
				}
				return
			}
			if err != nil || created.Session.WorkloadID != handle.ID || created.Session.WorkloadGeneration != 4 || created.Session.LifecycleMode != 2 {
				t.Fatalf("attachment lost exact facts: %#v %v", created.Session, err)
			}
			workloads.lookupErr = ports.ErrWorkloadUnsupported
			if _, err := service.Create(context.Background(), command); !errors.Is(err, domain.ErrWorkloadNotRunning) {
				t.Fatalf("replay minted token after program stopped: %v", err)
			}
		})
	}
}
func TestStaticAppAttachOnlyCreatesDeviceView(t *testing.T) {
	repository := newFakeRepository()
	resolver := &fakeResolver{descriptor: launchDescriptor()}
	service := newTestService(repository, resolver)
	command := validCommand("static-attach")
	command.AttachOnly = true
	if _, err := service.Create(context.Background(), command); err != nil {
		t.Fatal(err)
	}
}
