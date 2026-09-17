//go:build !faultinject

// Package faultinject has no runtime configuration in production builds.
package faultinject

import (
	"context"
	"net/http"
)

func Enabled() bool                            { return false }
func Arrive(context.Context, string)           {}
func SkipLeaseRenew() bool                     { return false }
func DropReply(next http.Handler) http.Handler { return next }
