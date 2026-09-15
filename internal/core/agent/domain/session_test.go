package domain

import (
	"strings"
	"testing"
	"time"
)

func newTestSession() *Session {
	return &Session{
		ID:          "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa",
		OwnerUserID: "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb",
		ProjectID:   "0199cccc-cccc-7ccc-8ccc-cccccccccccc",
		State:       SessionStateActive,
	}
}

func TestAcceptSessionInputSequencesAndQueueing(t *testing.T) {
	now := time.Now().UTC()
	session := newTestSession()

	first, queued, err := session.AcceptSessionInput(now, "input-1", "key-1", "first instruction")
	if err != nil || queued {
		t.Fatalf("first input: queued=%v err=%v", queued, err)
	}
	if first.Sequence != 1 || first.State != SessionInputAccepted {
		t.Fatalf("first input sequence/state: %d/%s", first.Sequence, first.State)
	}

	session.ActiveTaskID = "task-running"
	second, queued, err := session.AcceptSessionInput(now, "input-2", "key-2", "second instruction")
	if err != nil || !queued {
		t.Fatalf("second input should queue: queued=%v err=%v", queued, err)
	}
	if second.Sequence != 2 {
		t.Fatalf("second input sequence: %d", second.Sequence)
	}
}

func TestAcceptSessionInputRejectsClosedAndInvalid(t *testing.T) {
	now := time.Now().UTC()
	session := newTestSession()
	if err := session.CloseSession(now); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := session.AcceptSessionInput(now, "input-1", "key-1", "text"); err != ErrSessionClosed {
		t.Fatalf("closed session accepted input: %v", err)
	}

	open := newTestSession()
	if _, _, err := open.AcceptSessionInput(now, "input-1", "key-1", ""); err != ErrSessionInputInvalid {
		t.Fatalf("empty text accepted: %v", err)
	}
	if _, _, err := open.AcceptSessionInput(now, "input-1", "key-1", strings.Repeat("x", maximumSessionInputBytes+1)); err != ErrSessionInputInvalid {
		t.Fatalf("oversize text accepted: %v", err)
	}
}

func TestInputRequestDigestBindsKeyToContent(t *testing.T) {
	a := InputRequestDigest("key-1", "text")
	b := InputRequestDigest("key-1", "text")
	c := InputRequestDigest("key-1", "different text")
	if a != b {
		t.Fatal("same key and text produced different digests")
	}
	if a == c {
		t.Fatal("same key with different text produced the same digest")
	}
	if !strings.HasPrefix(a, "sha256:") || len(a) != len("sha256:")+64 {
		t.Fatalf("digest shape: %q", a)
	}
}

func TestDispatchGuardsSingleActiveExecution(t *testing.T) {
	now := time.Now().UTC()
	session := newTestSession()
	first, _, _ := session.AcceptSessionInput(now, "input-1", "key-1", "first")
	second, _, _ := session.AcceptSessionInput(now, "input-2", "key-2", "second")

	if err := session.DispatchSessionInput(&first, now, "task-1"); err != nil {
		t.Fatalf("dispatch first: %v", err)
	}
	if first.State != SessionInputDispatched || first.TaskID != "task-1" || session.ActiveTaskID != "task-1" {
		t.Fatal("dispatch did not bind the active execution")
	}
	if err := session.DispatchSessionInput(&second, now, "task-2"); err != ErrSessionBusy {
		t.Fatalf("concurrent dispatch allowed: %v", err)
	}
}

func TestFinishFreesSessionAndBoundsSummary(t *testing.T) {
	now := time.Now().UTC()
	session := newTestSession()
	input, _, _ := session.AcceptSessionInput(now, "input-1", "key-1", "first")
	_ = session.DispatchSessionInput(&input, now, "task-1")

	if err := session.FinishSessionInput(&input, now, SessionInputAccepted, "x"); err == nil {
		t.Fatal("non-terminal finish accepted")
	}
	long := strings.Repeat("s", maximumResultSummaryBytes+100)
	if err := session.FinishSessionInput(&input, now, SessionInputCompleted, long); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if len(input.ResultSummary) != maximumResultSummaryBytes {
		t.Fatalf("summary not bounded: %d", len(input.ResultSummary))
	}
	if session.ActiveTaskID != "" {
		t.Fatal("finish did not free the active execution")
	}
	next, queued, err := session.AcceptSessionInput(now, "input-2", "key-2", "next")
	if err != nil || queued {
		t.Fatalf("next input after finish: queued=%v err=%v", queued, err)
	}
	if err := session.DispatchSessionInput(&next, now, "task-2"); err != nil {
		t.Fatalf("dispatch next: %v", err)
	}
}

func TestFinishRejectsUndispatchedInput(t *testing.T) {
	now := time.Now().UTC()
	session := newTestSession()
	input, _, _ := session.AcceptSessionInput(now, "input-1", "key-1", "first")
	if err := session.FinishSessionInput(&input, now, SessionInputCompleted, "ok"); err == nil {
		t.Fatal("finished an accepted (undispatched) input")
	}
}

func TestCloseSessionIdempotence(t *testing.T) {
	now := time.Now().UTC()
	session := newTestSession()
	if err := session.CloseSession(now); err != nil {
		t.Fatalf("close: %v", err)
	}
	if session.State != SessionStateClosed || session.ClosedAt == nil {
		t.Fatal("close did not record terminal facts")
	}
	if err := session.CloseSession(now); err != ErrSessionClosed {
		t.Fatalf("double close: %v", err)
	}
}
