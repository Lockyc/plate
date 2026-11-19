package quality

import (
	"context"
	_ "embed"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/lockyc/plate/internal/engine"
)

//go:embed quartz.swift
var quartzSource []byte

func init() {
	Metrics["quartz-rmse"] = quartzRMSE
}

// quartzRender draws page of the PDF in through Quartz at dpi into out, a
// PNG, with a Swift helper compiled once per version into the cache.
func quartzRender(ctx context.Context, in string, page, dpi int, out string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("quartz-rmse needs macOS: Quartz is the renderer it measures")
	}
	bin, err := engine.SwiftHelper(ctx, "quartz", "quartz.swift", quartzSource)
	if err != nil {
		return err
	}
	_, err = engine.Run(ctx, engine.Cmd{Engine: "quartz", Path: bin, Args: []string{in, strconv.Itoa(page), strconv.Itoa(dpi), out}, Inputs: []string{in}, Outputs: []string{out}})
	return err
}

// quartzRMSE renders page (default 1) of the PDF pdf through Quartz, as
// macOS Preview draws it, at dpi (default 72), and compares it as rmse does
// over RGB with ref, an image of the same size: the source rendered by
// `plate pdf pages` at the same dpi. Quartz cannot be the reference itself,
// since it draws Chrome's own transparent gradients wrongly too.
func quartzRMSE(p Params) (float64, error) {
	in, err := p.Path("pdf")
	if err != nil {
		return 0, err
	}
	ref, err := p.Path("ref")
	if err != nil {
		return 0, err
	}
	page, err := p.Float("page", 1)
	if err != nil {
		return 0, err
	}
	dpi, err := p.Float("dpi", 72)
	if err != nil {
		return 0, err
	}
	tmp, err := os.MkdirTemp("", "quality-quartz-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)
	png := filepath.Join(tmp, "quartz.png")
	if err := quartzRender(context.Background(), in, int(page), int(dpi), png); err != nil {
		return 0, err
	}
	return rmse(Params{"a": png, "b": ref})
}
