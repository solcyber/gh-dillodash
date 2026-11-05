package fx

import "math"

// RGB is an 8-bit sRGB color.
type RGB struct{ R, G, B uint8 }

// OKLCH is the sRGB color at OKLab lightness l (0..1), chroma c and hue
// h (degrees). Out of gamut, the chroma comes down until it fits, so a
// hue never shifts and the lightness never moves: a color that cycles
// through every hue at one l keeps one brightness, which is what lets the
// release launch run the whole spectrum without a flash.
func OKLCH(l, c, h float64) RGB {
	rad := h * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	r, g, b, ok := oklabToLinear(l, c*cos, c*sin)
	if !ok {
		lo, hi := 0.0, c
		for i := 0; i < 14; i++ {
			mid := (lo + hi) / 2
			if _, _, _, ok := oklabToLinear(l, mid*cos, mid*sin); ok {
				lo = mid
			} else {
				hi = mid
			}
		}
		r, g, b, _ = oklabToLinear(l, lo*cos, lo*sin)
	}
	return RGB{encode(r), encode(g), encode(b)}
}

// oklabToLinear is OKLab to linear sRGB, and whether it is in gamut.
func oklabToLinear(L, a, b float64) (r, g, bl float64, ok bool) {
	l := L + 0.3963377774*a + 0.2158037573*b
	m := L - 0.1055613458*a - 0.0638541728*b
	s := L - 0.0894841775*a - 1.2914855480*b
	l, m, s = l*l*l, m*m*m, s*s*s
	r = 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g = -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	bl = -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	const eps = 1e-6
	ok = r >= -eps && r <= 1+eps && g >= -eps && g <= 1+eps && bl >= -eps && bl <= 1+eps
	return r, g, bl, ok
}

// OKLab is an sRGB color's OKLab lightness and a, b axes.
func OKLab(c RGB) (L, a, b float64) {
	r, g, bl := decode(c.R), decode(c.G), decode(c.B)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*bl)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*bl)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*bl)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s
}

// HueShift turns a color's hue by deg degrees at its own lightness and
// chroma.
func HueShift(c RGB, deg float64) RGB {
	L, a, b := OKLab(c)
	ch := math.Hypot(a, b)
	h := math.Atan2(b, a)*180/math.Pi + deg
	return OKLCH(L, ch, h)
}

// Luminance is a color's relative luminance (WCAG): 0 black, 1 white.
func Luminance(c RGB) float64 {
	return 0.2126*decode(c.R) + 0.7152*decode(c.G) + 0.0722*decode(c.B)
}

func decode(v uint8) float64 {
	x := float64(v) / 255
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

func encode(x float64) uint8 {
	x = math.Max(0, math.Min(1, x))
	if x <= 0.0031308 {
		x *= 12.92
	} else {
		x = 1.055*math.Pow(x, 1/2.4) - 0.055
	}
	return uint8(math.Round(x * 255))
}
