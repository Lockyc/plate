// Package vision runs Apple Vision's foreground segmentation through a small
// Swift helper, compiled once per helper version into the cache. It runs
// on-device: no image leaves the machine.
package vision

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/lockyc/plate/internal/engine"
)

//go:embed mask.swift
var source []byte

// Available refuses Vision where it cannot run: off macOS, or on a macOS
// before 14, whose Vision lacks foreground instance masks.
func Available() error { return available(runtime.GOOS, macOSVersion()) }

// available is Available for a given OS and macOS version ("" when unknown,
// which leaves swiftc and Vision to speak for themselves).
func available(goos, version string) error {
	if goos != "darwin" {
		return errors.New("--coarse vision needs macOS 14 or later (Apple Vision); use --coarse birefnet")
	}
	major, _, _ := strings.Cut(version, ".")
	if n, err := strconv.Atoi(major); err == nil && n < 14 {
		return fmt.Errorf("--coarse vision needs macOS 14 or later (Apple Vision), and this is macOS %s; use --coarse birefnet", version)
	}
	return nil
}

// Mask writes in's foreground, alpha as the mask, to out.
func Mask(ctx context.Context, in, out string) error {
	if err := Available(); err != nil {
		return err
	}
	bin, err := engine.SwiftHelper(ctx, "plate-vision", "mask.swift", source)
	if err != nil {
		return err
	}
	_, err = engine.Run(ctx, engine.Cmd{Engine: "plate-vision", Path: bin, Args: []string{in, out}, Inputs: []string{in}, Outputs: []string{out}, Timeout: 5 * time.Minute})
	return err
}
