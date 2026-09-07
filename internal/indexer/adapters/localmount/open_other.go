//go:build !linux

package localmount

import (
	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"os"
)

func openRoot(string) (*os.File, error) { return nil, failure(domain.DegradedUnavailable) }
func openDirectory(*os.File, string) (*os.File, error) {
	return nil, failure(domain.DegradedUnavailable)
}
func openFile(*os.File, string) (*os.File, error) { return nil, failure(domain.DegradedUnavailable) }
