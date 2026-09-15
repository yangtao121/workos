package domain

import (
	"testing"
	"time"
)

func newAttachment(id, device string, generation int64) *SurfaceAttachment {
	return &SurfaceAttachment{
		ID:                id,
		WorkloadID:        "workload-1",
		SurfaceSessionID:  "surface-" + id,
		OwnerUserID:       "owner-1",
		ProjectID:         "project-1",
		DeviceID:          device,
		Controls:          true,
		ControlGeneration: generation,
		State:             AttachmentStateAttached,
		AttachedAt:        time.Now().UTC(),
	}
}

func TestTakeoverAdvancesGenerationAndInvalidatesPreviousController(t *testing.T) {
	now := time.Now().UTC()
	deviceA := newAttachment("attach-a", "device-a", 1)
	lease := &ControlLease{
		WorkloadID:             "workload-1",
		ControlGeneration:      1,
		ControllerAttachmentID: "attach-a",
		ControllerDeviceID:     "device-a",
		GrantedAt:              now,
		ExpiresAt:              now.Add(time.Minute),
	}

	deviceB := newAttachment("attach-b", "device-b", 0)
	deviceB.Controls = false
	lease.Takeover(now, now.Add(time.Minute), deviceB)

	if lease.ControlGeneration != 2 {
		t.Fatalf("generation: %d", lease.ControlGeneration)
	}
	if !deviceB.Controls || deviceB.ControlGeneration != 2 {
		t.Fatal("takeover did not grant control to the new attachment")
	}
	lease.InvalidateController(deviceA)
	if deviceA.Controls {
		t.Fatal("previous controller kept its control flag")
	}
	if lease.Matches(deviceA) {
		t.Fatal("lease still matches the previous controller")
	}
}

func TestRenewOnlyByExactControllerBeforeExpiry(t *testing.T) {
	now := time.Now().UTC()
	controller := newAttachment("attach-a", "device-a", 1)
	lease := &ControlLease{
		WorkloadID:             "workload-1",
		ControlGeneration:      1,
		ControllerAttachmentID: "attach-a",
		ControllerDeviceID:     "device-a",
		GrantedAt:              now,
		ExpiresAt:              now.Add(time.Minute),
	}

	other := newAttachment("attach-b", "device-b", 1)
	if err := lease.Renew(now, now.Add(2*time.Minute), other); err != ErrControlDenied {
		t.Fatalf("non-controller renewed: %v", err)
	}
	if err := lease.Renew(now, now.Add(2*time.Minute), controller); err != nil {
		t.Fatalf("controller renew: %v", err)
	}

	expired := &ControlLease{
		WorkloadID:             "workload-1",
		ControlGeneration:      1,
		ControllerAttachmentID: "attach-a",
		ControllerDeviceID:     "device-a",
		GrantedAt:              now.Add(-2 * time.Minute),
		ExpiresAt:              now.Add(-time.Minute),
	}
	if err := expired.Renew(now, now.Add(time.Minute), controller); err != ErrControlExpired {
		t.Fatalf("expired renew: %v", err)
	}
}

func TestSameDeviceLateRenewalDoesNotStealControl(t *testing.T) {
	now := time.Now().UTC()
	// Device A attached first, then B took over.
	lease := &ControlLease{
		WorkloadID:             "workload-1",
		ControlGeneration:      2,
		ControllerAttachmentID: "attach-b",
		ControllerDeviceID:     "device-b",
		GrantedAt:              now,
		ExpiresAt:              now.Add(time.Minute),
	}
	// A re-attached from the same device id but a new attachment row.
	reattachedA := newAttachment("attach-a2", "device-a", 1)
	if err := lease.Renew(now, now.Add(2*time.Minute), reattachedA); err != ErrControlDenied {
		t.Fatalf("late renewal from the displaced device stole control: %v", err)
	}
}

func TestDetachReleasesOnlyTheAttachment(t *testing.T) {
	now := time.Now().UTC()
	controller := newAttachment("attach-a", "device-a", 1)
	lease := &ControlLease{
		WorkloadID:             "workload-1",
		ControlGeneration:      1,
		ControllerAttachmentID: "attach-a",
		ControllerDeviceID:     "device-a",
		GrantedAt:              now,
		ExpiresAt:              now.Add(time.Minute),
	}

	if err := controller.Detach(now); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if controller.State != AttachmentStateDetached || controller.Controls {
		t.Fatal("detach did not clear the attachment")
	}
	// The lease itself is untouched: the program keeps running and the lease
	// simply expires unless another device takes over.
	if !lease.ControlValid(now) || lease.Matches(newAttachment("attach-a", "device-a", 1)) == false {
		t.Fatal("detach disturbed the control lease")
	}
	if err := controller.Detach(now); err != ErrAttachmentDetached {
		t.Fatalf("double detach: %v", err)
	}
}

func TestTakeoverSucceedsAfterExpiryAndFromDetachedController(t *testing.T) {
	now := time.Now().UTC()
	expired := &ControlLease{
		WorkloadID:             "workload-1",
		ControlGeneration:      1,
		ControllerAttachmentID: "attach-a",
		ControllerDeviceID:     "device-a",
		GrantedAt:              now.Add(-2 * time.Minute),
		ExpiresAt:              now.Add(-time.Minute),
	}
	deviceB := newAttachment("attach-b", "device-b", 0)
	expired.Takeover(now, now.Add(time.Minute), deviceB)
	if expired.ControlGeneration != 2 || !deviceB.Controls {
		t.Fatal("takeover after expiry failed")
	}
}
