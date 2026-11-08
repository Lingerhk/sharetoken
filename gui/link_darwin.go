package main

// Wails uses UTType, but does not link the framework itself; `wails build`
// adds it on the command line. This keeps a plain `go build` working.

// #cgo LDFLAGS: -framework UniformTypeIdentifiers
import "C"
