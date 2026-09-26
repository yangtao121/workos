package resident

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

const maxRecordBytes = 2_200_000

type frameSubscriber struct {
	ch        chan []*surfacev1.GreenfieldWindowFrameTile
	needsFull bool
}

type broker struct {
	sessionID      string
	generation     int64
	listener       *net.UnixListener
	mu             sync.Mutex
	conn           *net.UnixConn
	closed         bool
	fatal          bool
	done           chan struct{}
	ready          chan struct{}
	readyOnce      sync.Once
	writeMu        sync.Mutex
	inputSlot      chan struct{}
	nextSubscriber uint64
	nextRequest    uint64
	pending        map[uint64]chan *surfacev1.GreenfieldChildEnvelope
	snapshot       *surfacev1.GreenfieldWindowSnapshot
	windowSubs     map[uint64]chan *surfacev1.GreenfieldWindowSnapshot
	frameSubs      map[string]map[uint64]*frameSubscriber
	windows        map[string]*surfacev1.GreenfieldWindow
	assemblies     map[string]*frameAssembly
	lastSequence   map[string]uint64
}

func newBroker(sessionID string, generation int64, dir string) (*broker, error) {
	path := filepath.Join(dir, "bridge.sock")
	if _, err := os.Lstat(path); err == nil {
		if active, dialErr := net.DialTimeout("unix", path, 150*time.Millisecond); dialErr == nil {
			active.Close()
			return nil, domain.ErrEngineUnavailable
		}
		if err := os.Remove(path); err != nil {
			return nil, domain.ErrEngineUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, domain.ErrEngineUnavailable
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, domain.ErrEngineUnavailable
	}
	if err := os.Chmod(path, 0700); err != nil {
		listener.Close()
		return nil, domain.ErrEngineUnavailable
	}
	b := &broker{sessionID: sessionID, generation: generation, listener: listener,
		done: make(chan struct{}), ready: make(chan struct{}), inputSlot: make(chan struct{}, 1),
		pending:    map[uint64]chan *surfacev1.GreenfieldChildEnvelope{},
		windowSubs: map[uint64]chan *surfacev1.GreenfieldWindowSnapshot{},
		frameSubs:  map[string]map[uint64]*frameSubscriber{}, windows: map[string]*surfacev1.GreenfieldWindow{},
		assemblies: map[string]*frameAssembly{}, lastSequence: map[string]uint64{},
	}
	b.inputSlot <- struct{}{}
	go b.accept()
	return b, nil
}

func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var uid uint32
	var controlErr error
	err = raw.Control(func(fd uintptr) {
		credentials, getErr := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if getErr != nil {
			controlErr = getErr
			return
		}
		uid = credentials.Uid
	})
	if controlErr != nil {
		return 0, controlErr
	}
	return uid, err
}

func (b *broker) accept() {
	for {
		conn, err := b.listener.AcceptUnix()
		if err != nil {
			return
		}
		uid, err := peerUID(conn)
		if err != nil || uid != 10001 {
			conn.Close()
			continue
		}
		b.mu.Lock()
		if b.closed || b.fatal || b.conn != nil {
			b.mu.Unlock()
			conn.Close()
			continue
		}
		b.conn = conn
		b.mu.Unlock()
		err = b.readLoop(conn)
		b.mu.Lock()
		wasClosed := b.closed
		b.conn = nil
		b.mu.Unlock()
		conn.Close()
		if !wasClosed {
			b.fail(err)
		}
		return
	}
}

func readEnvelope(r io.Reader) (*surfacev1.GreenfieldChildEnvelope, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > maxRecordBytes {
		return nil, domain.ErrEngineUnavailable
	}
	raw := make([]byte, length)
	if _, err := io.ReadFull(r, raw); err != nil {
		return nil, err
	}
	envelope := &surfacev1.GreenfieldChildEnvelope{}
	if err := proto.Unmarshal(raw, envelope); err != nil {
		return nil, domain.ErrEngineUnavailable
	}
	return envelope, nil
}

func writeEnvelope(w io.Writer, envelope *surfacev1.GreenfieldChildEnvelope) error {
	raw, err := proto.Marshal(envelope)
	if err != nil || len(raw) == 0 || len(raw) > maxRecordBytes {
		return domain.ErrEngineUnavailable
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	for _, part := range [][]byte{header[:], raw} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

func (b *broker) readLoop(conn *net.UnixConn) error {
	for {
		envelope, err := readEnvelope(conn)
		if err != nil {
			return err
		}
		if envelope.GetProtocolVersion() != 1 || envelope.GetSessionId() != b.sessionID || envelope.GetWorkloadGeneration() != b.generation {
			return domain.ErrEngineUnavailable
		}
		switch payload := envelope.Payload.(type) {
		case *surfacev1.GreenfieldChildEnvelope_Windows:
			if envelope.GetRequestId() != 0 {
				return domain.ErrEngineUnavailable
			}
			if err := b.acceptSnapshot(payload.Windows); err != nil {
				return err
			}
		case *surfacev1.GreenfieldChildEnvelope_FrameTile:
			if envelope.GetRequestId() != 0 {
				return domain.ErrEngineUnavailable
			}
			if err := b.acceptTile(payload.FrameTile); err != nil {
				return err
			}
		case *surfacev1.GreenfieldChildEnvelope_InputResult,
			*surfacev1.GreenfieldChildEnvelope_ClipboardResult:
			if envelope.GetRequestId() == 0 {
				return domain.ErrEngineUnavailable
			}
			b.mu.Lock()
			waiter := b.pending[envelope.GetRequestId()]
			b.mu.Unlock()
			if waiter == nil {
				return domain.ErrEngineUnavailable
			}
			select {
			case waiter <- envelope:
			default:
				return domain.ErrEngineUnavailable
			}
		case *surfacev1.GreenfieldChildEnvelope_Failure:
			if envelope.GetRequestId() != 0 || payload.Failure == nil {
				return domain.ErrEngineUnavailable
			}
			return domain.ErrEngineUnavailable
		default:
			return domain.ErrEngineUnavailable
		}
	}
}

func (b *broker) waitReady(ctx context.Context) error {
	select {
	case <-b.ready:
		return nil
	case <-b.done:
		return domain.ErrEngineUnavailable
	case <-ctx.Done():
		return domain.ErrEngineUnavailable
	}
}

func (b *broker) failed() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.fatal }

func (b *broker) fail(_ error) {
	b.mu.Lock()
	if b.closed || b.fatal {
		b.mu.Unlock()
		return
	}
	b.fatal = true
	close(b.done)
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	b.listener.Close()
}

func (b *broker) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if !b.fatal {
		close(b.done)
	}
	conn := b.conn
	b.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	b.listener.Close()
}

func (b *broker) aroundInput(ctx context.Context, action func() error) error {
	select {
	case <-b.inputSlot:
		defer func() { b.inputSlot <- struct{}{} }()
		if b.failed() {
			return domain.ErrEngineUnavailable
		}
		return action()
	case <-b.done:
		return domain.ErrEngineUnavailable
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *broker) aroundControl(ctx context.Context, action func() error) error {
	select {
	case <-b.inputSlot:
		defer func() { b.inputSlot <- struct{}{} }()
		return action()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *broker) exchange(ctx context.Context, envelope *surfacev1.GreenfieldChildEnvelope) (*surfacev1.GreenfieldChildEnvelope, error) {
	b.mu.Lock()
	if b.closed || b.fatal || b.conn == nil {
		b.mu.Unlock()
		return nil, domain.ErrEngineUnavailable
	}
	b.nextRequest++
	id := b.nextRequest
	envelope.RequestId = id
	envelope.ProtocolVersion = 1
	envelope.SessionId = b.sessionID
	envelope.WorkloadGeneration = b.generation
	waiter := make(chan *surfacev1.GreenfieldChildEnvelope, 1)
	b.pending[id] = waiter
	conn := b.conn
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, id); b.mu.Unlock() }()
	b.writeMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := writeEnvelope(conn, envelope)
	b.writeMu.Unlock()
	if err != nil {
		b.fail(err)
		return nil, domain.ErrEngineUnavailable
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case answer := <-waiter:
		return answer, nil
	case <-timer.C:
		b.fail(domain.ErrEngineUnavailable)
		return nil, domain.ErrEngineUnavailable
	case <-b.done:
		return nil, domain.ErrEngineUnavailable
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *broker) sendEvent(ctx context.Context, request *surfacev1.SendGreenfieldWindowInputRequest) (*surfacev1.SendGreenfieldWindowInputResponse, error) {
	answer, err := b.exchange(ctx, &surfacev1.GreenfieldChildEnvelope{Payload: &surfacev1.GreenfieldChildEnvelope_Input{Input: request}})
	if err != nil {
		return nil, err
	}
	result := answer.GetInputResult()
	if result == nil {
		b.fail(domain.ErrEngineUnavailable)
		return nil, domain.ErrEngineUnavailable
	}
	return result, nil
}

func (b *broker) readClipboard(ctx context.Context, request *surfacev1.ReadGreenfieldClipboardRequest) (*surfacev1.ReadGreenfieldClipboardResponse, error) {
	answer, err := b.exchange(ctx, &surfacev1.GreenfieldChildEnvelope{Payload: &surfacev1.GreenfieldChildEnvelope_ClipboardRead{ClipboardRead: request}})
	if err != nil {
		return nil, err
	}
	result := answer.GetClipboardResult()
	if result == nil {
		b.fail(domain.ErrEngineUnavailable)
		return nil, domain.ErrEngineUnavailable
	}
	return result, nil
}
