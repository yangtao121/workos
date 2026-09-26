package ports

import (
	"context"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

// ResidentLaunch is an immutable, server-derived native workload descriptor.
// Generation is the durable workload generation, never a browser lease epoch.
type ResidentLaunch struct {
	Session   domain.Session
	Workspace WorkspaceGrant
}

// ResidentEngine owns the isolated child across Runtime process restarts.
// Adoption must verify Docker identity, image and the complete security
// profile before returning a Display; absence is ErrResidentChildNotFound.
type ResidentEngine interface {
	Engine
	LaunchResident(context.Context, ResidentLaunch) (Display, error)
	AdoptResident(context.Context, ResidentLaunch) (Display, error)
	// ReapResident removes only the child identified by the durable session
	// identity, even when this Runtime has no live broker after a restart.
	ReapResident(context.Context, ResidentLaunch) error
}

// ChildIdentity is durable evidence of the exact child created for a native
// generation. It is persisted before the session is reported running.
type ChildIdentity struct {
	ContainerID string
	ImageID     string
	Generation  int64
}

type ResidentDisplay interface {
	Display
	ChildIdentity() ChildIdentity
	// PreserveForAdoption closes this process's IPC broker without stopping the
	// child. Explicit Stop and lifecycle expiry still remove the child.
	PreserveForAdoption()
}

type ResidentStore interface {
	BindChild(context.Context, string, string, ChildIdentity) error
	RestartReceipt(context.Context, string, string, string) (int64, bool, error)
	StopReceipt(context.Context, string, string, string) (bool, error)
}

type ExactStopper interface{ StopExact(context.Context) error }

// WindowAuthorizer reads Surface-owned attachment and control facts. Native
// application checks its own session state/generation independently.
type WindowAuthorizer interface {
	AuthorizeWindowViewer(context.Context, string, string, string, string, int64) error
	AuthorizeWindowController(context.Context, string, string, string, string, int64, int64) error
}

type InputBarrier interface {
	AroundInput(context.Context, func() error) error
}

// WindowDisplay exports immutable protocol facts produced by the private
// child. Only the native application authorizes and opens these subscriptions.
type WindowDisplay interface {
	Display
	InputBarrier
	SubscribeWindows() (<-chan *surfacev1.GreenfieldWindowSnapshot, func(), error)
	SubscribeFrames(string) (<-chan []*surfacev1.GreenfieldWindowFrameTile, func(), error)
	SendWindowEvent(context.Context, *surfacev1.SendGreenfieldWindowInputRequest) (*surfacev1.SendGreenfieldWindowInputResponse, error)
	ReadWindowClipboard(context.Context, *surfacev1.ReadGreenfieldClipboardRequest) (*surfacev1.ReadGreenfieldClipboardResponse, error)
}
