package resident

import (
	"bytes"
	"context"
	"image/png"
	"slices"
	"time"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"google.golang.org/protobuf/proto"
)

const (
	maxWindows       = 128
	maxSnapshotBytes = 1 << 20
	maxTitleBytes    = 4 << 10
	maxTileSide      = 512
	maxTileBytes     = 2 << 20
	maxFrameSide     = 4096
	maxFrameTiles    = 64
	maxFrameBytes    = 24 << 20
)

type frameAssembly struct {
	sequence   uint64
	revision   uint64
	count      uint32
	full       bool
	width      uint32
	height     uint32
	renderedAt time.Time
	parts      []*surfacev1.GreenfieldWindowFrameTile
	bytes      int
}

func validRect(rect *surfacev1.GreenfieldWindowRect) bool {
	return rect != nil && rect.GetWidth() > 0 && rect.GetHeight() > 0 && rect.GetWidth() <= maxFrameSide && rect.GetHeight() <= maxFrameSide
}

func (b *broker) acceptSnapshot(snapshot *surfacev1.GreenfieldWindowSnapshot) error {
	if snapshot == nil || snapshot.GetSessionId() != b.sessionID || snapshot.GetWorkloadGeneration() != b.generation ||
		len(snapshot.GetWindows()) > maxWindows || proto.Size(snapshot) > maxSnapshotBytes ||
		(snapshot.GetState() != surfacev1.GreenfieldDisplayState_GREENFIELD_DISPLAY_STATE_RUNNING &&
			snapshot.GetState() != surfacev1.GreenfieldDisplayState_GREENFIELD_DISPLAY_STATE_FAILED &&
			snapshot.GetState() != surfacev1.GreenfieldDisplayState_GREENFIELD_DISPLAY_STATE_STOPPED) {
		return domain.ErrEngineUnavailable
	}
	windows := make(map[string]*surfacev1.GreenfieldWindow, len(snapshot.Windows))
	for _, window := range snapshot.Windows {
		if window == nil || !domain.ValidUUIDv7(window.GetId()) || len(window.GetTitle()) > maxTitleBytes || len(window.GetAppId()) > maxTitleBytes ||
			!validRect(window.GetContentRect()) || !validRect(window.GetVisualRect()) ||
			window.GetDevicePixelRatioMillis() < 500 || window.GetDevicePixelRatioMillis() > 4000 || window.GetRevision() == 0 {
			return domain.ErrEngineUnavailable
		}
		if _, exists := windows[window.GetId()]; exists {
			return domain.ErrEngineUnavailable
		}
		windows[window.GetId()] = window
	}
	for _, window := range windows {
		if parent := window.GetParentWindowId(); parent != "" {
			if parent == window.GetId() || windows[parent] == nil {
				return domain.ErrEngineUnavailable
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.fatal {
		return domain.ErrEngineUnavailable
	}
	if b.snapshot != nil && snapshot.GetRevision() <= b.snapshot.GetRevision() {
		if snapshot.GetRevision() == b.snapshot.GetRevision() && proto.Equal(snapshot, b.snapshot) {
			return nil
		}
		return domain.ErrEngineUnavailable
	}
	for id, subscribers := range b.frameSubs {
		previous := b.windows[id]
		current := windows[id]
		if current == nil {
			for _, sub := range subscribers {
				close(sub.ch)
			}
			delete(b.frameSubs, id)
			delete(b.assemblies, id)
			delete(b.lastSequence, id)
			continue
		}
		if previous == nil || previous.GetRevision() != current.GetRevision() {
			delete(b.assemblies, id)
			delete(b.lastSequence, id)
			for _, sub := range subscribers {
				sub.needsFull = true
				select {
				case <-sub.ch:
				default:
				}
			}
		}
	}
	b.windows = windows
	b.snapshot = snapshot
	for _, subscriber := range b.windowSubs {
		select {
		case subscriber <- snapshot:
		default:
			select {
			case <-subscriber:
			default:
			}
			subscriber <- snapshot
		}
	}
	b.readyOnce.Do(func() { close(b.ready) })
	if snapshot.GetState() != surfacev1.GreenfieldDisplayState_GREENFIELD_DISPLAY_STATE_RUNNING {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func validateTile(tile *surfacev1.GreenfieldWindowFrameTile, window *surfacev1.GreenfieldWindow, generation int64) error {
	if tile == nil || window == nil || tile.GetWindowId() != window.GetId() || tile.GetWorkloadGeneration() != generation ||
		tile.GetWindowRevision() != window.GetRevision() || tile.GetFrameSequence() == 0 || tile.GetTileCount() == 0 ||
		tile.GetTileCount() > maxFrameTiles || tile.GetTileIndex() >= tile.GetTileCount() ||
		tile.GetWidth() == 0 || tile.GetHeight() == 0 || tile.GetWidth() > maxTileSide || tile.GetHeight() > maxTileSide ||
		tile.GetFrameWidth() == 0 || tile.GetFrameHeight() == 0 || tile.GetFrameWidth() > maxFrameSide || tile.GetFrameHeight() > maxFrameSide ||
		tile.GetWidth() > tile.GetFrameWidth() || tile.GetHeight() > tile.GetFrameHeight() ||
		tile.GetX() > tile.GetFrameWidth()-tile.GetWidth() || tile.GetY() > tile.GetFrameHeight()-tile.GetHeight() ||
		len(tile.GetPng()) == 0 || len(tile.GetPng()) > maxTileBytes || tile.GetRenderedAt() == nil || tile.GetRenderedAt().CheckValid() != nil {
		return domain.ErrEngineUnavailable
	}
	if !bytes.HasPrefix(tile.GetPng(), []byte("\x89PNG\r\n\x1a\n")) {
		return domain.ErrEngineUnavailable
	}
	config, err := png.DecodeConfig(bytes.NewReader(tile.GetPng()))
	if err != nil || config.Width != int(tile.GetWidth()) || config.Height != int(tile.GetHeight()) {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func (b *broker) acceptTile(tile *surfacev1.GreenfieldWindowFrameTile) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.fatal || b.snapshot == nil || b.snapshot.GetState() != surfacev1.GreenfieldDisplayState_GREENFIELD_DISPLAY_STATE_RUNNING {
		return domain.ErrEngineUnavailable
	}
	window := b.windows[tile.GetWindowId()]
	if err := validateTile(tile, window, b.generation); err != nil {
		return err
	}
	if tile.GetFrameSequence() <= b.lastSequence[tile.GetWindowId()] {
		return domain.ErrEngineUnavailable
	}
	assembly := b.assemblies[tile.GetWindowId()]
	if assembly == nil || tile.GetFrameSequence() > assembly.sequence {
		if assembly != nil {
			for _, sub := range b.frameSubs[tile.GetWindowId()] {
				sub.needsFull = true
			}
		}
		assembly = &frameAssembly{sequence: tile.GetFrameSequence(), revision: tile.GetWindowRevision(), count: tile.GetTileCount(), full: tile.GetFullRefresh(),
			width: tile.GetFrameWidth(), height: tile.GetFrameHeight(), renderedAt: tile.GetRenderedAt().AsTime(), parts: make([]*surfacev1.GreenfieldWindowFrameTile, tile.GetTileCount())}
		b.assemblies[tile.GetWindowId()] = assembly
	}
	if tile.GetFrameSequence() != assembly.sequence || tile.GetWindowRevision() != assembly.revision ||
		tile.GetTileCount() != assembly.count || tile.GetFullRefresh() != assembly.full ||
		tile.GetFrameWidth() != assembly.width || tile.GetFrameHeight() != assembly.height ||
		!tile.GetRenderedAt().AsTime().Equal(assembly.renderedAt) || assembly.parts[tile.GetTileIndex()] != nil {
		return domain.ErrEngineUnavailable
	}
	assembly.parts[tile.GetTileIndex()] = tile
	assembly.bytes += len(tile.GetPng())
	if assembly.bytes > maxFrameBytes {
		return domain.ErrEngineUnavailable
	}
	if slices.Contains(assembly.parts, (*surfacev1.GreenfieldWindowFrameTile)(nil)) {
		return nil
	}
	if !validTileGeometry(assembly) {
		return domain.ErrEngineUnavailable
	}
	delete(b.assemblies, tile.GetWindowId())
	b.lastSequence[tile.GetWindowId()] = tile.GetFrameSequence()
	for _, sub := range b.frameSubs[tile.GetWindowId()] {
		if sub.needsFull && !assembly.full {
			continue
		}
		select {
		case sub.ch <- assembly.parts:
			sub.needsFull = false
		default:
			select {
			case <-sub.ch:
			default:
			}
			sub.needsFull = true
			if assembly.full {
				sub.ch <- assembly.parts
				sub.needsFull = false
			}
		}
	}
	return nil
}

func validTileGeometry(frame *frameAssembly) bool {
	var total uint64
	for i, a := range frame.parts {
		total += uint64(a.GetWidth()) * uint64(a.GetHeight())
		for _, other := range frame.parts[:i] {
			if a.GetX() < other.GetX()+other.GetWidth() && other.GetX() < a.GetX()+a.GetWidth() &&
				a.GetY() < other.GetY()+other.GetHeight() && other.GetY() < a.GetY()+a.GetHeight() {
				return false
			}
		}
	}
	return !frame.full || total == uint64(frame.width)*uint64(frame.height)
}

func (b *broker) subscribeWindows() (<-chan *surfacev1.GreenfieldWindowSnapshot, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.fatal {
		return nil, nil, domain.ErrEngineUnavailable
	}
	if len(b.windowSubs) >= maxWindows {
		return nil, nil, domain.ErrSessionLimit
	}
	b.nextSubscriber++
	id := b.nextSubscriber
	ch := make(chan *surfacev1.GreenfieldWindowSnapshot, 1)
	b.windowSubs[id] = ch
	if b.snapshot != nil {
		ch <- b.snapshot
	}
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.windowSubs[id]; ok {
			delete(b.windowSubs, id)
			close(ch)
		}
		b.mu.Unlock()
	}, nil
}

func (b *broker) subscribeFrames(windowID string) (<-chan []*surfacev1.GreenfieldWindowFrameTile, func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.fatal || b.windows[windowID] == nil {
		return nil, nil, domain.ErrEngineUnavailable
	}
	if len(b.frameSubs[windowID]) >= 4 {
		return nil, nil, domain.ErrSessionLimit
	}
	b.nextSubscriber++
	id := b.nextSubscriber
	sub := &frameSubscriber{ch: make(chan []*surfacev1.GreenfieldWindowFrameTile, 1), needsFull: true}
	if b.frameSubs[windowID] == nil {
		b.frameSubs[windowID] = map[uint64]*frameSubscriber{}
	}
	b.frameSubs[windowID][id] = sub
	return sub.ch, func() {
		b.mu.Lock()
		if bucket := b.frameSubs[windowID]; bucket != nil {
			if _, ok := bucket[id]; ok {
				delete(bucket, id)
				close(sub.ch)
			}
		}
		b.mu.Unlock()
	}, nil
}

func (d *display) SubscribeWindows() (<-chan *surfacev1.GreenfieldWindowSnapshot, func(), error) {
	return d.broker.subscribeWindows()
}
func (d *display) SubscribeFrames(id string) (<-chan []*surfacev1.GreenfieldWindowFrameTile, func(), error) {
	return d.broker.subscribeFrames(id)
}
func (d *display) SendWindowEvent(ctx context.Context, request *surfacev1.SendGreenfieldWindowInputRequest) (*surfacev1.SendGreenfieldWindowInputResponse, error) {
	return d.broker.sendEvent(ctx, request)
}
func (d *display) ReadWindowClipboard(ctx context.Context, request *surfacev1.ReadGreenfieldClipboardRequest) (*surfacev1.ReadGreenfieldClipboardResponse, error) {
	return d.broker.readClipboard(ctx, request)
}
