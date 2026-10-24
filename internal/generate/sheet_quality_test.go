//go:build quality

package generate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lockyc/plate/internal/imgsize"
)

// TestSheetPNG needs Chrome: it renders the sheet and checks the PNG is the
// page's width and as tall as its content, not the tall canvas it was cut from.
func TestSheetPNG(t *testing.T) {
	needMagick(t)
	root := t.TempDir()
	pose := filepath.Join(root, "wave")
	ref := writeRef(t, filepath.Join(root), "front.png")
	seedRound(t, pose, 1, "model-one", "a wave", 0.10, 0.10, 0.10, 0.10)
	seedRound(t, pose, 2, "model-two", "another wave", 0.10)
	if omitted, err := WriteSheet(ctx, pose, []string{ref}); err != nil || omitted != 0 {
		t.Fatalf("omitted %d, err %v", omitted, err)
	}
	for _, name := range []string{"sheet.html", "sheet.png"} {
		if _, err := os.Stat(filepath.Join(pose, name)); err != nil {
			t.Error(err)
		}
	}
	w, h, err := imgsize.Dims(filepath.Join(pose, "sheet.png"))
	if err != nil {
		t.Fatal(err)
	}
	if w != sheetWidth {
		t.Errorf("sheet.png is %d wide, want %d", w, sheetWidth)
	}
	if h < 600 || h >= sheetTall {
		t.Errorf("sheet.png is %d tall, want the content's height (under %d)", h, sheetTall)
	}
	t.Logf("sheet.png %dx%d", w, h)
}
