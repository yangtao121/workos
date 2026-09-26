package main

import (
	"testing"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

func TestNativeWorkloadCarriesPersistedApplicationName(t *testing.T) {
	runtime := &surfaceInteractiveRuntime{}
	for _, test := range []struct {
		application domain.Application
		want        string
	}{
		{domain.ApplicationCode, "WorkOS Code"},
		{domain.ApplicationTextEditor, "Text Editor"},
		{domain.Application("legacy_unknown"), ""},
	} {
		t.Run(string(test.application), func(t *testing.T) {
			workload := runtime.nativeWorkload(domain.Session{Application: test.application})
			if workload.DisplayName != test.want {
				t.Fatalf("native application %q display name = %q, want %q", test.application, workload.DisplayName, test.want)
			}
		})
	}
}
