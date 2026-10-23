//go:build !linux && !darwin

package cli

import (
	"context"
	"errors"
	"os"
)

func isTerminal(*os.File) bool { return false }

func makeRaw(*os.File) (func(), error) {
	return nil, errors.New("a terminal session needs Linux or macOS")
}

func termSize(*os.File) (uint32, uint32) { return 0, 0 }

func resizes(context.Context) <-chan struct{} { return nil }
