// Command screenshot renders the README's picture and the docs' gallery:
// the dashboard on the demo fixture, drawn offline with the embedded Nerd
// Font and the theme's exact colors, so every image is the terminal pixel
// for pixel without a terminal. The hero (docs/screenshot.png) is the
// list over a pull request's detail; the gallery (docs/shots/<name>.png)
// is one picture per shot in shots.go.
//
//	go run ./cmd/screenshot              # everything
//	go run ./cmd/screenshot -only release-card,help
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/solcyber/gh-dillodash/internal/canvas"
	"github.com/solcyber/gh-dillodash/internal/demo"
	"github.com/solcyber/gh-dillodash/internal/fonts"
	"github.com/solcyber/gh-dillodash/internal/fx"
	"github.com/solcyber/gh-dillodash/internal/glyphs"
	"github.com/solcyber/gh-dillodash/internal/text"
	"github.com/solcyber/gh-dillodash/internal/theme"
	"github.com/solcyber/gh-dillodash/internal/ui"
	"github.com/solcyber/gh-dillodash/internal/workflow"

	_ "github.com/solcyber/gh-dillodash/internal/workflow/all"
)

func main() {
	out := flag.String("out", "docs/screenshot.png", "where to write the hero PNG (the list over a pull request's detail)")
	outDir := flag.String("out-dir", "docs/shots", "where the gallery's PNGs go, one per shot")
	only := flag.String("only", "", "render only these shots (comma-separated names); \"hero\" is the README picture")
	size := flag.Float64("size", 26, "font size in pixels")
	flag.Parse()
	want := map[string]bool{}
	for _, n := range strings.Split(*only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	pick := func(name string) bool { return len(want) == 0 || want[name] }
	regular, bold := faces(*size)
	if pick("hero") {
		setGlyphs("nerd")
		frames, err := render(shot{Name: "hero"}, [][]string{
			{"j"},                    // the list, cursor on the second row
			{"j", "j", "j", "enter"}, // a pull request's detail
		})
		if err != nil {
			die(err)
		}
		write(*out, frames, 128, 24, regular, bold)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		die(err)
	}
	for _, s := range shots {
		if !pick(s.Name) {
			continue
		}
		setGlyphs(s.glyphSet())
		frames, err := render(s, [][]string{s.Steps})
		if err != nil {
			die(fmt.Errorf("%s: %w", s.Name, err))
		}
		cols, rows := s.size()
		write(filepath.Join(*outDir, s.Name+".png"), frames, cols, rows, regular, bold)
	}
}

// setGlyphs picks the glyph set and the theme for a shot.
func setGlyphs(set string) {
	switch set {
	case "ascii":
		ui.UseGlyphs(glyphs.ASCII)
	case "unicode":
		ui.UseGlyphs(glyphs.Unicode)
	default:
		ui.UseGlyphs(glyphs.Nerd)
	}
	theme.Setup(colorprofile.TrueColor, false)
	fx.Motion = "off"
}

// render drives the demo fixture through the shot's scripts.
func render(s shot, scripts [][]string) ([]*canvas.Canvas, error) {
	cfg := demo.Config()
	wf, err := workflow.Load(cfg, cfg.Reviews.Repos)
	if err != nil {
		return nil, err
	}
	cols, rows := s.size()
	return ui.SnapshotsWith(context.Background(), ui.Options{
		Config: cfg, GH: demo.Client(), Workflows: wf, Me: demo.Me, Host: "github.com", Version: "dev",
	}, s.Opts, cols, rows, scripts)
}

// write paints the frames, stacked with a rule between them, into a PNG.
func write(path string, frames []*canvas.Canvas, cols, rows int, regular, bold font.Face) {
	cw, ch, ascent := metrics(regular)
	pad := 18
	gap := 12
	W := cols*cw + 2*pad
	H := len(frames)*(rows*ch) + (len(frames)-1)*gap + 2*pad
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, draw.Src)
	for i, c := range frames {
		top := pad + i*(rows*ch+gap)
		paint(img, c, pad, top, cw, ch, ascent, regular, bold)
		if i > 0 { // a faint rule between the frames
			y := top - gap/2
			for x := pad; x < W-pad; x++ {
				img.Set(x, y, color.RGBA{40, 44, 52, 255})
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		die(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%dx%d)\n", path, W, H)
}

func faces(size float64) (regular, bold font.Face) {
	var fs []font.Face
	for _, f := range fonts.Files() {
		sf, err := opentype.Parse(f.Data)
		if err != nil {
			die(err)
		}
		face, err := opentype.NewFace(sf, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			die(err)
		}
		fs = append(fs, face)
	}
	return fs[0], fs[1]
}

func metrics(face font.Face) (cw, ch, ascent int) {
	adv, _ := face.GlyphAdvance('M')
	m := face.Metrics()
	return adv.Ceil(), m.Height.Ceil(), m.Ascent.Ceil()
}

var sgrRGB = regexp.MustCompile(`(38|48);2;(\d+);(\d+);(\d+)`)

func paint(img *image.RGBA, c *canvas.Canvas, left, top, cw, ch, ascent int, regular, bold font.Face) {
	black := color.RGBA{0, 0, 0, 255}
	c.Walk(func(y, x int, cell canvas.CellInfo) {
		var fg, bg color.Color = color.RGBA{204, 204, 204, 255}, black
		isBold, underline, reverse := false, false, false
		if cell.Raw != "" {
			for _, m := range sgrRGB.FindAllStringSubmatch(cell.Raw, -1) {
				r, _ := strconv.Atoi(m[2])
				g, _ := strconv.Atoi(m[3])
				b, _ := strconv.Atoi(m[4])
				col := color.RGBA{uint8(r), uint8(g), uint8(b), 255}
				if m[1] == "38" {
					fg = col
				} else {
					bg = col
				}
			}
		} else {
			st := theme.Style(cell.Style, cell.Sel, cell.Band)
			if col := st.GetForeground(); col != (lipgloss.NoColor{}) {
				fg = col
			}
			if col := st.GetBackground(); col != (lipgloss.NoColor{}) {
				bg = col
			}
			isBold = st.GetBold() || cell.Extra&text.Bold != 0
			underline = st.GetUnderline() || cell.Extra&text.Underline != 0
			reverse = st.GetReverse() || cell.Extra&text.Reverse != 0
		}
		if reverse {
			fg, bg = bg, fg
		}
		x0, y0 := left+x*cw, top+y*ch
		w := cw * cell.Width
		draw.Draw(img, image.Rect(x0, y0, x0+w, y0+ch), image.NewUniform(bg), image.Point{}, draw.Src)
		if strings.TrimSpace(cell.Glyph) == "" {
			return
		}
		face := regular
		if isBold {
			face = bold
		}
		d := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: face,
			Dot: fixed.Point26_6{X: fixed.I(x0), Y: fixed.I(y0 + ascent)}}
		d.DrawString(cell.Glyph)
		if underline {
			yy := y0 + ch - 2
			for xx := x0; xx < x0+w; xx++ {
				img.Set(xx, yy, fg)
			}
		}
	})
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "screenshot:", err)
	os.Exit(1)
}
