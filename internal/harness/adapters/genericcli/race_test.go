//go:build race

package genericcli

// Race-instrumented test binaries start the helper subprocess noticeably
// slower, so the fixture timeouts scale instead of flaking under -race.
const helperTimeoutScale = 5
