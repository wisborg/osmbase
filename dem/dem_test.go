package dem

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// encode is a Terrarium PNG of size by size pixels with height h(i, j).
func encode(t *testing.T, size int, h func(i, j int) float64) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for j := 0; j < size; j++ {
		for i := 0; i < size; i++ {
			v := h(i, j) + 32768
			r := int(v / 256)
			g := int(v) - r*256
			b := int((v - float64(int(v))) * 256)
			img.SetNRGBA(i, j, color.NRGBA{uint8(r), uint8(g), uint8(b), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecode_ReadsTerrariumHeights(t *testing.T) {
	data := encode(t, 4, func(i, j int) float64 { return float64(i)*100 - float64(j)*0.5 - 10 })
	g, err := Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if g.Size != 4 {
		t.Fatalf("size %d, want 4", g.Size)
	}
	for _, c := range []struct {
		i, j int
		want float32
	}{{0, 0, -10}, {3, 0, 290}, {0, 3, -11.5}, {2, 1, 189.5}} {
		if got := g.At(c.i, c.j); got != c.want {
			t.Errorf("At(%d, %d) = %g, want %g", c.i, c.j, got, c.want)
		}
	}
}

func TestDecode_RefusesWhatIsNotAnElevationTile(t *testing.T) {
	if _, err := Decode([]byte("\x1a\x45\xdf\xa3 not an image")); err == nil {
		t.Error("decoded bytes that are neither WebP nor PNG")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	var buf bytes.Buffer
	png.Encode(&buf, img)
	if _, err := Decode(buf.Bytes()); !errors.Is(err, ErrNotSquare) {
		t.Errorf("a 4 by 2 tile: %v, want ErrNotSquare", err)
	}
}

type tiles map[[3]uint32][]byte

func (m tiles) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	b, ok := m[[3]uint32{uint32(z), x, y}]
	return b, ok, nil
}

type counting struct {
	tiles
	reads int
}

func (c *counting) Tile(z uint8, x, y uint32) ([]byte, bool, error) {
	c.reads++
	return c.tiles.Tile(z, x, y)
}

func TestSource_DecodesOnceAndSaysWhatIsMissing(t *testing.T) {
	src := &counting{tiles: tiles{{3, 1, 2}: encode(t, 2, func(i, j int) float64 { return 7 })}}
	s := NewSource(src)
	for range 3 {
		h, n, ok, err := s.Heights(3, 1, 2)
		if err != nil || !ok || n != 2 || h[3] != 7 {
			t.Fatalf("Heights = %v, %d, %v, %v", h, n, ok, err)
		}
	}
	if _, _, ok, err := s.Heights(3, 0, 0); ok || err != nil {
		t.Errorf("a missing tile: ok %v, err %v; want not ok and no error", ok, err)
	}
	s.Heights(3, 0, 0)
	if src.reads != 2 {
		t.Errorf("%d reads, want 2: one per tile, a miss remembered as well as a hit", src.reads)
	}
}

func TestSource_ForgetsTheOldestPastItsLimit(t *testing.T) {
	src := &counting{tiles: tiles{}}
	s := NewSource(src)
	s.limit = 2
	for x := range uint32(3) {
		s.Heights(1, x, 0)
	}
	s.Heights(1, 2, 0) // still held
	s.Heights(1, 0, 0) // forgotten
	if src.reads != 4 {
		t.Errorf("%d reads, want 4", src.reads)
	}
}

func TestSource_ReportsATileThatWillNotDecode(t *testing.T) {
	s := NewSource(tiles{{0, 0, 0}: []byte("RIFF\x00\x00\x00\x00WEBPgarbage")})
	if _, _, _, err := s.Heights(0, 0, 0); err == nil {
		t.Error("a corrupt tile decoded")
	}
}
