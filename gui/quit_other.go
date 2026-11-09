//go:build !linux

package main

import "context"

// onBeforeClose is only needed on Linux; see quit_linux.go.
var onBeforeClose func(context.Context) bool
