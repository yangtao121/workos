package transport

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
)

const watchOwner = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
const watchSession = "0198d7ea-2110-7c42-b659-c5e4d73bc338"

type watchRepository struct {
	ports.SessionRepository
	mu      sync.Mutex
	events  []domain.SessionEvent
	revoked bool
}

func (r *watchRepository) GetSession(_ context.Context, owner, id string) (domain.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked || owner != watchOwner || id != watchSession {
		return domain.Session{}, domain.ErrSessionNotFound
	}
	return domain.Session{ID: id, OwnerUserID: owner}, nil
}
func (r *watchRepository) ListEvents(_ context.Context, _ string, after int64, limit int) ([]domain.SessionEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.SessionEvent
	for _, event := range r.events {
		if event.Sequence > after && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}
func (r *watchRepository) append(sequence int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, domain.SessionEvent{SessionID: watchSession, Sequence: sequence, EventType: "input_accepted", Payload: []byte(`{"input_id":"input"}`), OccurredAt: time.Now().UTC()})
}

func TestSessionWatchCatchupFollowAndRevocation(t *testing.T) {
	repo := &watchRepository{}
	repo.append(1)
	service := application.NewSessionService(repo, nil, nil, nil)
	_, handler := NewSessionHandler(service, nil)
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := agentv1connect.NewAgentSessionServiceClient(server.Client(), server.URL)
	request := func(after int64, follow bool) *connect.Request[agentv1.WatchSessionEventsRequest] {
		r := connect.NewRequest(&agentv1.WatchSessionEventsRequest{SessionId: watchSession, After: after, Follow: follow})
		r.Header().Set(identity.UserHeader, watchOwner)
		r.Header().Set(identity.DeviceHeader, watchOwner)
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	catchup, err := client.WatchSessionEvents(ctx, request(0, false))
	if err != nil {
		t.Fatal(err)
	}
	if !catchup.Receive() || len(catchup.Msg().Events) != 1 || catchup.Msg().Events[0].Sequence != 1 {
		t.Fatal("missing legacy catch-up")
	}
	if catchup.Receive() || catchup.Err() != nil {
		t.Fatal("legacy stream must end after catch-up", catchup.Err())
	}
	catchup.Close()
	live, err := client.WatchSessionEvents(ctx, request(0, true))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	if !live.Receive() {
		t.Fatal(live.Err())
	}
	repo.append(2)
	if !live.Receive() || len(live.Msg().Events) != 1 || live.Msg().Events[0].Sequence != 2 {
		t.Fatal("live stream failed to observe a later input", live.Err())
	}
	repo.mu.Lock()
	repo.revoked = true
	repo.mu.Unlock()
	if live.Receive() || connect.CodeOf(live.Err()) != connect.CodeNotFound {
		t.Fatal("watch failed to recheck ownership", live.Err())
	}
}

func TestSessionWatchRejectsInvalidCursorAndForeignSession(t *testing.T) {
	repo := &watchRepository{}
	_, handler := NewSessionHandler(application.NewSessionService(repo, nil, nil, nil), nil)
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := agentv1connect.NewAgentSessionServiceClient(server.Client(), server.URL)
	for _, tc := range []struct {
		owner string
		after int64
		code  connect.Code
	}{{watchOwner, -1, connect.CodeInvalidArgument}, {watchSession, 0, connect.CodeNotFound}} {
		req := connect.NewRequest(&agentv1.WatchSessionEventsRequest{SessionId: watchSession, After: tc.after, Follow: true})
		req.Header().Set(identity.UserHeader, tc.owner)
		req.Header().Set(identity.DeviceHeader, watchOwner)
		stream, err := client.WatchSessionEvents(context.Background(), req)
		if err == nil {
			stream.Receive()
			err = stream.Err()
			stream.Close()
		}
		if connect.CodeOf(err) != tc.code {
			t.Fatalf("got %v want %v", err, tc.code)
		}
	}
}
