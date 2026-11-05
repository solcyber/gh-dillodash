package fx

import (
	"math"
	"testing"
)

func TestOKLCHRoundTrips(t *testing.T) {
	for _, c := range []RGB{{0, 0, 0}, {255, 255, 255}, {255, 0, 0}, {12, 200, 90}, {40, 60, 230}} {
		L, a, b := OKLab(c)
		got := OKLCH(L, math.Hypot(a, b), math.Atan2(b, a)*180/math.Pi)
		for _, d := range []int{int(got.R) - int(c.R), int(got.G) - int(c.G), int(got.B) - int(c.B)} {
			if d < -1 || d > 1 {
				t.Errorf("%v -> %v", c, got)
			}
		}
	}
}

// A full turn of hue at one lightness stays at one brightness: that is
// the launch's whole flash guard, so pin it.
func TestOKLCHHueKeepsLuminance(t *testing.T) {
	for _, lc := range [][2]float64{{0.34, 0.10}, {0.52, 0.14}, {0.80, 0.15}, {0.90, 0.13}} {
		lo, hi := 2.0, -1.0
		for h := 0.0; h < 360; h += 5 {
			y := Luminance(OKLCH(lc[0], lc[1], h))
			lo, hi = math.Min(lo, y), math.Max(hi, y)
		}
		if hi-lo > 0.1 {
			t.Errorf("L=%.2f: luminance spans %.3f..%.3f over the hues", lc[0], lo, hi)
		}
	}
}

func TestHueShiftKeepsLightness(t *testing.T) {
	c := RGB{0xf7, 0xe0, 0x34}
	L0, _, _ := OKLab(c)
	for d := 0.0; d < 360; d += 30 {
		L, _, _ := OKLab(HueShift(c, d))
		if math.Abs(L-L0) > 0.02 {
			t.Errorf("shift %v: L %.3f, want %.3f", d, L, L0)
		}
	}
}
