//go:build (!windows && !linux) || (linux && !cgo)

package main

import (
	"errors"
	"runtime"
)

func loadNativeTicketLibrary(string) (nativeTicketLibrary, error) {
	return nil, errors.New("Steam encrypted app ticket library is unsupported on " + runtime.GOOS + "/" + runtime.GOARCH)
}

func defaultNativeTicketLibraryPath() string {
	return ""
}
