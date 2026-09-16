// Input event mapping (ADR-0029): bounded JSON events from the workos.input
// data channel become xdotool XTEST injections on the session's display. A
// token bucket bounds the event rate; overflow is dropped and counted.
package xvfbengine

import (
	"context"
	"log/slog"
	"math"
	"os/exec"
	"strconv"
	"time"
	"unicode/utf8"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"google.golang.org/protobuf/encoding/protojson"
)

// keyAllowlist keeps XTEST injection to an explicit, finite key set; anything
// else (arbitrary keysyms, shell metacharacters) is rejected outright.
var keyAllowlist = map[string]bool{
	"Return": true, "BackSpace": true, "Delete": true, "Escape": true, "Tab": true,
	"Home": true, "End": true, "Page_Up": true, "Page_Down": true,
	"Left": true, "Right": true, "Up": true, "Down": true,
	"F1": true, "F2": true, "F3": true, "F4": true, "F5": true, "F6": true,
	"F7": true, "F8": true, "F9": true, "F10": true, "F11": true, "F12": true,
	"ctrl+c": true, "ctrl+d": true, "ctrl+l": true, "ctrl+u": true,
	"ctrl+a": true, "ctrl+e": true, "ctrl+w": true, "ctrl+z": true,
	"shift+Tab": true,
}

// enqueueInput admits one bounded event subject to the rate budget; the
// single worker (started in Launch) preserves ordering like a real input
// device.
func (d *display) enqueueInput(raw []byte) {
	if d.Exited() || len(raw) == 0 || len(raw) > domain.MaxInputEvent {
		return
	}
	now := time.Now()
	d.inputMu.Lock()
	elapsed := now.Sub(d.inputStamp).Seconds()
	if elapsed > 0 {
		d.inputTokens += elapsed * float64(domain.InputRatePerSec)
		if d.inputTokens > float64(domain.InputBurst) {
			d.inputTokens = float64(domain.InputBurst)
		}
		d.inputStamp = now
	}
	if d.inputTokens < 1 {
		d.inputDropped++
		d.inputMu.Unlock()
		return
	}
	d.inputTokens--
	d.inputMu.Unlock()

	select {
	case d.inputQueue <- queuedInput{raw: append([]byte(nil), raw...), epoch: d.peerEpoch}:
	default:
		d.inputMu.Lock()
		d.inputDropped++
		d.inputMu.Unlock()
	}
}

func (d *display) applyInput(raw []byte) {
	var event surfacev1.NativeInputEvent
	if err := protojson.Unmarshal(raw, &event); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(d.runCtx, xdotoolTimeout)
	defer cancel()
	var argv []string
	switch event.Type {
	case "text":
		if !validText(event.Text) || event.Key != "" || event.Action != "" || event.X != 0 || event.Y != 0 || event.Button != 0 {
			return
		}
		argv = []string{"type", "--clearmodifiers", "--delay", "0", "--", event.Text}
	case "key":
		if !keyAllowlist[event.Key] || event.Text != "" || event.Action != "" || event.X != 0 || event.Y != 0 || event.Button != 0 {
			return
		}
		argv = []string{"key", "--clearmodifiers", "--", event.Key}
	case "pointer":
		if event.Text != "" || event.Key != "" {
			return
		}
		argv = d.pointerArgv(&event)
	default:
		return
	}
	if argv == nil {
		return
	}
	cmd := exec.CommandContext(ctx, d.engine.Xdotool, argv...)
	cmd.Env = []string{"DISPLAY=" + d.displayName, "HOME=" + d.dir, "PATH=/usr/local/bin:/usr/bin:/bin"}
	if err := cmd.Run(); err != nil {
		slog.Warn("native input injection failed", "display", d.displayName, "type", event.Type, "error", err)
		return
	}
	slog.Info("native input injected", "display", d.displayName, "type", event.Type)
}

// validText admits printable runes plus newline and tab only, bounded to the
// documented per-event run budget.
func validText(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	count := 0
	for _, r := range text {
		count++
		if count > domain.MaxTextRunes {
			return false
		}
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return count > 0
}

// pointerArgv maps normalized coordinates to the display pixel space; the
// action set is exactly move/down/up/click with buttons 1..3. Do not use
// mousemove --sync: a repeated coordinate emits no motion event and would
// block the ordered input worker until its timeout.
func (d *display) pointerArgv(event *surfacev1.NativeInputEvent) []string {
	if math.IsNaN(event.X) || math.IsNaN(event.Y) || event.X < 0 || event.X > 1 || event.Y < 0 || event.Y > 1 {
		return nil
	}
	x := int(event.X * float64(d.width-1))
	y := int(event.Y * float64(d.height-1))
	validButton := event.Button >= 1 && event.Button <= 3
	switch event.Action {
	case "move":
		return []string{"mousemove", strconv.Itoa(x), strconv.Itoa(y)}
	case "down":
		if !validButton {
			return nil
		}
		return []string{"mousemove", strconv.Itoa(x), strconv.Itoa(y), "mousedown", strconv.Itoa(int(event.Button))}
	case "up":
		if !validButton {
			return nil
		}
		return []string{"mousemove", strconv.Itoa(x), strconv.Itoa(y), "mouseup", strconv.Itoa(int(event.Button))}
	case "click":
		if !validButton {
			return nil
		}
		return []string{"mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", strconv.Itoa(int(event.Button))}
	default:
		return nil
	}
}
