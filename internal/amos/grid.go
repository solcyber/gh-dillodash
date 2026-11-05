package amos

// Grid is the rendering's art pixels as palette indices (0 transparent;
// see Palette), one row per pixel, Size(o) wide and twice as tall plus
// the bob row: for a caller that recolors Amos before painting him (the
// release launch tumbles him through every hue).
func Grid(o Options) [][]uint8 { return grid(o) }
