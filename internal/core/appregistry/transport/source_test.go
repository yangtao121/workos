package transport

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	"github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type sourceRecorder struct{ calls int }

func (r *sourceRecorder) CreateSource(_ context.Context, bundle domain.SourceBundle) (domain.SourceBundle, error) {
	r.calls++
	return bundle, nil
}
func (*sourceRecorder) GetSource(context.Context, string, string) (domain.SourceBundle, error) {
	return domain.SourceBundle{}, domain.ErrNotFound
}

func TestSourceWireBudgetAndIdentity(t *testing.T) {
	repository := &sourceRecorder{}
	service, err := application.NewSourceService(repository, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	path, handler := NewSourceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, identity.Middleware(handler))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, test := range []struct {
		name          string
		options       []connect.ClientOption
		size          int
		authenticated bool
		code          connect.Code
	}{
		{"binary boundary", nil, domain.MaxSourceFileBytes, true, 0},
		{"JSON boundary", []connect.ClientOption{connect.WithProtoJSON()}, domain.MaxSourceFileBytes, true, 0},
		{"oversize", nil, 1024 * 1024, true, connect.CodeResourceExhausted},
		{"gzip bomb", []connect.ClientOption{connect.WithSendGzip()}, 1024 * 1024, true, connect.CodeResourceExhausted},
		{"missing identity", nil, 1, false, connect.CodeUnauthenticated},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := appv1connect.NewAppSourceBundleServiceClient(server.Client(), server.URL, test.options...)
			request := connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: "wire", Files: []*appv1.AppSourceFile{
				{Path: "a", Content: bytes.Repeat([]byte{'a'}, test.size)},
				{Path: "b", Content: bytes.Repeat([]byte{'b'}, test.size)},
			}})
			if test.authenticated {
				request.Header().Set(identity.UserHeader, ids.UUIDv7{}.New())
				request.Header().Set(identity.DeviceHeader, ids.UUIDv7{}.New())
			}
			before := repository.calls
			response, err := client.CreateAppSourceBundle(context.Background(), request)
			if test.code == 0 {
				if err != nil {
					t.Fatal(err)
				}
				if response.Msg.GetBundle().GetTotalSizeBytes() != domain.MaxSourceTotalBytes || repository.calls != before+1 {
					t.Fatal("legal maximum did not reach storage intact")
				}
			} else if connect.CodeOf(err) != test.code || repository.calls != before {
				t.Fatalf("expected %v before persistence, got %v; calls=%d", test.code, err, repository.calls-before)
			}
		})
	}
}
