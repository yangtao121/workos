//go:build !linux

package workspace

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// No path-based fallback: the required Linux pathname protection is absent.
type Store struct{}

func New([]Mount) (*Store, error)                  { return nil, domain.ErrUnavailable }
func (*Store) Close()                              {}
func (*Store) Access(ports.FileScope) (bool, bool) { return false, false }
func (*Store) List(context.Context, ports.FileScope, string, string) (domain.FilePage, error) {
	return domain.FilePage{}, domain.ErrUnavailable
}
func (*Store) Read(context.Context, ports.FileScope, domain.FileRef) ([]byte, error) {
	return nil, domain.ErrUnavailable
}
func (*Store) Write(context.Context, ports.FileScope, domain.FileRef, []byte) (domain.FileRef, error) {
	return domain.FileRef{}, domain.ErrUnavailable
}
