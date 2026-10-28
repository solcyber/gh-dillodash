// Package fortress is the dashboard's Dwarf Fortress map (the d key): an
// easter egg you can read. Every open PR, issue, branch and recent
// default-branch commit is drawn on an ASCII map of Manhattan or Chicago
// (a coin flip each time). The encoding is fixed:
//
//	row   = age: the numbered street is the age in days since it was
//	        opened (Manhattan: 14th St = 14 days, older is north; Chicago:
//	        35th St = 35 days, older is south of Madison)
//	lane  = repo: the land at each row is cut into one lane per repo
//	shape = what it is; color + motion = its state (Legend)
//
// Window-queued items sleep in the barracks, releases trade at the depot.
// The map reads only what the last refresh fetched: it makes no calls.
//
// This one view uses Dwarf Fortress's own 16 colors (Colors), so it is
// the exception to the grays-blues-golds palette; every state also has
// its own shape, and --motion off / mono draw the status glyph itself.
// Text on it (legend, look panel, announcements) uses the ordinary theme
// styles.
//
// Geography is hand-traced (lat, lon) projected at load: Manhattan onto
// its street grid (rotated 29 degrees; one unit = one 264 ft block),
// Chicago onto its address grid (hundreds from State and Madison; one
// unit = one street).
package fortress

import (
	"math"
	"sort"
	"sync"
)

// FtPerDegLat is the feet in one degree of latitude.
const FtPerDegLat = 364000.0

var (
	mh0Lat, mh0Lon = 40.7253, -73.9965    // Broadway and Houston: street 0
	mhFtLon        = FtPerDegLat * 0.7573 // cos(40.73 deg)
	mhSin, mhCos   = 0.4848, 0.8746       // the grid's 29 degrees
)

// ManhattanXY projects a (lat, lon) into Manhattan grid units: (east,
// street number).
func ManhattanXY(lat, lon float64) (x, y float64) {
	dx := (lon - mh0Lon) * mhFtLon
	dy := (lat - mh0Lat) * FtPerDegLat
	return (dx*mhCos - dy*mhSin) / 264.0, (dx*mhSin + dy*mhCos) / 264.0
}

// latitude -> Chicago address hundreds (N +, S -); the south side's
// numbered streets are not evenly spaced, so interpolate between real ones
var chiLat = [][2]float64{
	{41.6100, -160}, {41.6445, -138}, {41.6773, -119}, {41.6929, -111},
	{41.7219, -95}, {41.7509, -79}, {41.7801, -63}, {41.7948, -55},
	{41.8092, -47}, {41.8309, -35}, {41.8380, -31}, {41.8527, -22},
	{41.8675, -12}, {41.8819, 0}, {41.8966, 8}, {41.9108, 16},
	{41.9253, 24}, {41.9399, 32}, {41.9700, 50},
}

// ChicagoXY projects a (lat, lon) into Chicago address units: (hundreds
// east of State, hundreds north of Madison).
func ChicagoXY(lat, lon float64) (x, y float64) {
	x = (lon + 87.6278) / 0.01937 * 8
	pts := chiLat
	var a, b [2]float64
	switch {
	case lat <= pts[0][0]:
		a, b = pts[0], pts[1]
	case lat >= pts[len(pts)-1][0]:
		a, b = pts[len(pts)-2], pts[len(pts)-1]
	default:
		for i := 1; i < len(pts); i++ {
			if pts[i][0] >= lat {
				a, b = pts[i-1], pts[i]
				break
			}
		}
	}
	y = a[1] + (lat-a[0])/(b[0]-a[0])*(b[1]-a[1])
	return x, y
}

// LL is a hand-traced (lat, lon).
type LL struct{ Lat, Lon float64 }

// XY is a point in a city's grid units.
type XY struct{ X, Y float64 }

// Shape is a traced polygon (or polyline) with its terrain code: w water,
// o land outside the city, L city land, p park, B a road, = a bridge or
// pier, # elevated rail; - is a labelled street, added at raster time.
type Shape struct {
	Code byte
	Name string
	Pts  []LL
}

// Street is a labelled street: its grid y and the margin label.
type Street struct {
	Y     int
	Label string
}

// Label is a map caption at a (lat, lon).
type Label struct {
	At   LL
	Text string
}

// Zone is a box in grid units (x0, x1, y0, y1) items can stand in.
type Zone struct{ X0, X1, Y0, Y1 float64 }

// City is one of the two maps.
type City struct {
	Key, Name string
	Proj      func(lat, lon float64) (x, y float64)
	// Dir is +1 when older is a higher y (Manhattan), -1 when lower.
	Dir int
	// Limit is the last street: anything older sits "beyond".
	Limit int
	Base  byte // the terrain under everything: w or o
	// Y0, Y1 are the grid y range the map covers.
	Y0, Y1 float64
	Zero   string // the street that is today
	Older  string // which way older is
	Beyond string // where the too-old go
	// Barracks (queued), Depot (releases) and Far (merged releases).
	Barracks, Depot, Far string
	Shapes               []Shape
	Lines                []Shape
	Streets              []Street
	Labels               []Label
	Zones                map[string]Zone

	once sync.Once
	proj *Projected
}

// ProjShape is a Shape in grid units.
type ProjShape struct {
	Code byte
	Name string
	Pts  []XY
}

// ProjLabel is a Label in grid units.
type ProjLabel struct {
	At   XY
	Text string
}

// Projected is a city's shapes and lines in its grid units.
type Projected struct {
	Shapes, Lines []ProjShape
	Labels        []ProjLabel
	// MainX0, MainX1 bound the city proper (the L shape) in x.
	MainX0, MainX1 float64
}

// Projected returns the city's geometry projected into grid units (cached).
func (c *City) Projected() *Projected {
	c.once.Do(func() {
		p := &Projected{}
		conv := func(shapes []Shape) []ProjShape {
			out := make([]ProjShape, 0, len(shapes))
			for _, s := range shapes {
				ps := ProjShape{Code: s.Code, Name: s.Name}
				for _, ll := range s.Pts {
					x, y := c.Proj(ll.Lat, ll.Lon)
					ps.Pts = append(ps.Pts, XY{x, y})
				}
				out = append(out, ps)
			}
			return out
		}
		p.Shapes = conv(c.Shapes)
		p.Lines = conv(c.Lines)
		for _, l := range c.Labels {
			x, y := c.Proj(l.At.Lat, l.At.Lon)
			p.Labels = append(p.Labels, ProjLabel{XY{x, y}, l.Text})
		}
		p.MainX0, p.MainX1 = math.Inf(1), math.Inf(-1)
		for _, s := range p.Shapes {
			if s.Code != 'L' {
				continue
			}
			for _, pt := range s.Pts {
				p.MainX0 = math.Min(p.MainX0, pt.X)
				p.MainX1 = math.Max(p.MainX1, pt.X)
			}
			break
		}
		c.proj = p
	})
	return c.proj
}

// scanSpans is the even-odd fill: the x intervals of polygon pts on the
// line at y.
func scanSpans(pts []XY, y float64) [][2]float64 {
	var xs []float64
	n := len(pts)
	for i := 0; i < n; i++ {
		a, b := pts[i], pts[(i+1)%n]
		if (a.Y <= y && y < b.Y) || (b.Y <= y && y < a.Y) {
			xs = append(xs, a.X+(y-a.Y)*(b.X-a.X)/(b.Y-a.Y))
		}
	}
	sort.Float64s(xs)
	var out [][2]float64
	for i := 0; i+1 < len(xs); i += 2 {
		out = append(out, [2]float64{xs[i], xs[i+1]})
	}
	return out
}

// CityKeys lists the cities, in coin-flip order.
var CityKeys = []string{"manhattan", "chicago"}

// Cities holds the two maps by key.
var Cities = map[string]*City{
	"manhattan": {
		Key: "manhattan", Name: "Manhattan", Proj: ManhattanXY, Dir: 1, Limit: 220,
		Base: 'w', Y0: -48, Y1: 238, Zero: "Houston St",
		Older: "north", Beyond: "the Bronx",
		Barracks: "Battery Park", Depot: "the Seaport", Far: "Brooklyn",
		Shapes: []Shape{
			{'o', "New Jersey", []LL{{40.640, -74.400}, {40.640, -74.075},
				{40.700, -74.050}, {40.716, -74.0335},
				{40.727, -74.030}, {40.737, -74.028},
				{40.747, -74.0255}, {40.757, -74.021},
				{40.767, -74.0145}, {40.780, -74.004},
				{40.795, -73.995}, {40.812, -73.982},
				{40.830, -73.966}, {40.851, -73.953},
				{40.871, -73.941}, {40.900, -73.925},
				{40.990, -73.900}, {40.990, -74.400}}},
			{'o', "Brooklyn", []LL{{40.560, -74.030}, {40.640, -74.030},
				{40.676, -74.018}, {40.692, -74.001},
				{40.701, -73.995}, {40.704, -73.990},
				{40.702, -73.976}, {40.709, -73.968},
				{40.716, -73.966}, {40.730, -73.961},
				{40.737, -73.962}, {40.560, -73.700}}},
			{'o', "Queens", []LL{{40.737, -73.962}, {40.745, -73.958},
				{40.756, -73.948}, {40.765, -73.939},
				{40.772, -73.933}, {40.779, -73.924},
				{40.787, -73.912}, {40.790, -73.700},
				{40.560, -73.700}}},
			{'o', "the Bronx", []LL{{40.799, -73.700}, {40.800, -73.915},
				{40.806, -73.926}, {40.813, -73.930},
				{40.821, -73.9325}, {40.829, -73.931},
				{40.842, -73.9265}, {40.847, -73.9245},
				{40.856, -73.917}, {40.864, -73.9115},
				{40.873, -73.908}, {40.880, -73.915},
				{40.882, -73.924}, {40.900, -73.917},
				{40.990, -73.900}, {40.990, -73.700}}},
			{'o', "Randalls Island", []LL{{40.785, -73.930}, {40.796, -73.929},
				{40.801, -73.920}, {40.800, -73.915},
				{40.790, -73.915}, {40.782, -73.923}}},
			{'o', "Roosevelt Island", []LL{{40.7520, -73.9630},
				{40.7700, -73.9440},
				{40.7715, -73.9405},
				{40.7540, -73.9595}}},
			{'o', "Governors Island", []LL{{40.687, -74.023}, {40.693, -74.017},
				{40.6915, -74.011}, {40.685, -74.017}}},
			{'L', "Manhattan", []LL{
				{40.7003, -74.0150}, {40.7045, -74.0185}, {40.7080, -74.0195},
				{40.7135, -74.0180}, {40.7190, -74.0150}, {40.7260, -74.0120},
				{40.7330, -74.0110}, {40.7400, -74.0105}, {40.7480, -74.0090},
				{40.7560, -74.0060}, {40.7640, -74.0010}, {40.7720, -73.9950},
				{40.7810, -73.9890}, {40.7900, -73.9830}, {40.8010, -73.9745},
				{40.8120, -73.9665}, {40.8215, -73.9590}, {40.8320, -73.9520},
				{40.8420, -73.9475}, {40.8510, -73.9475}, {40.8600, -73.9395},
				{40.8680, -73.9330}, {40.8750, -73.9290}, {40.8790, -73.9235},
				{40.8770, -73.9170}, {40.8740, -73.9110}, {40.8690, -73.9100},
				{40.8640, -73.9150}, {40.8560, -73.9200}, {40.8460, -73.9265},
				{40.8420, -73.9290}, {40.8340, -73.9320}, {40.8285, -73.9335},
				{40.8205, -73.9350}, {40.8130, -73.9325}, {40.8070, -73.9290},
				{40.8030, -73.9285}, {40.7960, -73.9300}, {40.7880, -73.9370},
				{40.7800, -73.9420}, {40.7755, -73.9425}, {40.7680, -73.9495},
				{40.7600, -73.9575}, {40.7525, -73.9645}, {40.7480, -73.9675},
				{40.7420, -73.9715}, {40.7360, -73.9740}, {40.7290, -73.9725},
				{40.7200, -73.9735}, {40.7135, -73.9765}, {40.7110, -73.9790},
				{40.7090, -73.9870}, {40.7075, -73.9950}, {40.7065, -74.0005},
				{40.7045, -74.0030}, {40.7020, -74.0090}, {40.7010, -74.0125}}},
			{'p', "Central Park", []LL{{40.7644, -73.9730}, {40.7968, -73.9496},
				{40.8005, -73.9581}, {40.7681, -73.9819}}},
			{'p', "Morningside Park", []LL{{40.8013, -73.9592},
				{40.8022, -73.9572},
				{40.8115, -73.9518},
				{40.8108, -73.9542}}},
			{'p', "Riverside Park", []LL{{40.7810, -73.9890}, {40.8215, -73.9590},
				{40.8212, -73.9568},
				{40.7805, -73.9862}}},
			{'p', "Fort Tryon Park", []LL{{40.8560, -73.9370}, {40.8630, -73.9345},
				{40.8655, -73.9300}, {40.8590, -73.9300}}},
			{'p', "Inwood Hill Park", []LL{{40.8640, -73.9300}, {40.8700, -73.9330},
				{40.8770, -73.9270}, {40.8730, -73.9200},
				{40.8680, -73.9220}}},
			{'p', "Highbridge Park", []LL{{40.8290, -73.9350}, {40.8460, -73.9285},
				{40.8580, -73.9225}, {40.8590, -73.9250},
				{40.8470, -73.9305}, {40.8300, -73.9370}}},
			{'p', "Battery Park", []LL{{40.7003, -74.0150}, {40.7045, -74.0185},
				{40.7055, -74.0150}, {40.7035, -74.0130},
				{40.7010, -74.0125}}},
		},
		Lines: []Shape{
			{'B', "Broadway", []LL{{40.7056, -74.0137}, {40.7128, -74.0070},
				{40.7253, -73.9965}, {40.7359, -73.9911},
				{40.7411, -73.9897}, {40.7497, -73.9880},
				{40.7580, -73.9855}, {40.7681, -73.9819},
				{40.7785, -73.9819}, {40.7940, -73.9720},
				{40.8040, -73.9665}, {40.8155, -73.9585},
				{40.8260, -73.9500}, {40.8406, -73.9399},
				{40.8490, -73.9350}, {40.8625, -73.9250},
				{40.8690, -73.9150}, {40.8735, -73.9115}}},
			{'=', "the Brooklyn Bridge", []LL{{40.7090, -74.0010}, {40.7030, -73.9900}}},
			{'=', "the Manhattan Bridge", []LL{{40.7135, -73.9945}, {40.7000, -73.9865}}},
			{'=', "the Williamsburg Bridge", []LL{{40.7155, -73.9800}, {40.7110, -73.9620}}},
			{'=', "the Queensboro Bridge", []LL{{40.7605, -73.9620}, {40.7525, -73.9385}}},
			{'=', "the Triborough Bridge", []LL{{40.8015, -73.9310}, {40.7975, -73.9225}}},
			{'=', "the George Washington Bridge", []LL{{40.8515, -73.9450}, {40.8490, -73.9640}}},
		},
		Streets: []Street{{0, "Houston"}, {14, "14th"}, {23, "23rd"}, {34, "34th"},
			{42, "42nd"}, {59, "59th"}, {72, "72nd"}, {86, "86th"},
			{96, "96th"}, {110, "110th"}, {125, "125th"},
			{145, "145th"}, {155, "155th"}, {181, "181st"},
			{207, "207th"}, {220, "220th"}},
		Labels: []Label{{LL{40.770, -74.012}, "HUDSON"},
			{LL{40.725, -73.968}, "EAST R."},
			{LL{40.700, -74.150}, "NEW JERSEY"},
			{LL{40.680, -73.950}, "BROOKLYN"},
			{LL{40.755, -73.900}, "QUEENS"},
			{LL{40.870, -73.880}, "THE BRONX"}},
		Zones: map[string]Zone{"barracks": {-99, 99, -40, -24},
			"depot":  {-99, 99, -24, -4},
			"far":    {2, 99, -40, -16},
			"beyond": {-99, 99, 223, 236}},
	},
	"chicago": {
		Key: "chicago", Name: "Chicago", Proj: ChicagoXY, Dir: -1, Limit: 138,
		Base: 'o', Y0: -150, Y1: 28, Zero: "Madison St",
		Older: "south", Beyond: "Indiana",
		Barracks: "River North", Depot: "Navy Pier", Far: "the pier",
		Shapes: []Shape{
			{'w', "Lake Michigan", []LL{{41.990, -87.6555}, {41.9840, -87.6500},
				{41.9800, -87.6460}, {41.9690, -87.6380}, {41.9640, -87.6305},
				{41.9600, -87.6350}, {41.9500, -87.6370}, {41.9420, -87.6355},
				{41.9330, -87.6310}, {41.9250, -87.6290}, {41.9118, -87.6250},
				{41.9030, -87.6230}, {41.8990, -87.6180}, {41.8935, -87.6115},
				{41.8890, -87.6120}, {41.8840, -87.6130}, {41.8800, -87.6150},
				{41.8740, -87.6150}, {41.8670, -87.6145}, {41.8600, -87.6110},
				{41.8510, -87.6120}, {41.8389, -87.6074}, {41.8250, -87.6020},
				{41.8130, -87.5925}, {41.8020, -87.5850}, {41.7950, -87.5770},
				{41.7910, -87.5800}, {41.7860, -87.5750}, {41.7790, -87.5700},
				{41.7700, -87.5630}, {41.7620, -87.5530}, {41.7540, -87.5440},
				{41.7400, -87.5330}, {41.7310, -87.5280}, {41.7290, -87.5260},
				{41.7086, -87.5246}, {41.6800, -87.4950}, {41.6500, -87.4300},
				{41.6500, -87.0000}, {41.9900, -87.0000}}},
			{'L', "Chicago", []LL{{41.9500, -87.6370}, {41.9500, -87.8040},
				{41.9108, -87.8040}, {41.9108, -87.7750}, {41.8675, -87.7750},
				{41.8675, -87.7446}, {41.8237, -87.7446}, {41.8237, -87.8040},
				{41.7766, -87.8040}, {41.7766, -87.7446}, {41.7509, -87.7400},
				{41.7509, -87.7060}, {41.6773, -87.7060}, {41.6773, -87.6470},
				{41.6445, -87.6470}, {41.6445, -87.5246}, {41.7086, -87.5246},
				{41.7290, -87.5260}, {41.7310, -87.5280}, {41.7400, -87.5330},
				{41.7540, -87.5440}, {41.7620, -87.5530}, {41.7700, -87.5630},
				{41.7790, -87.5700}, {41.7860, -87.5750}, {41.7910, -87.5800},
				{41.7950, -87.5770}, {41.8020, -87.5850}, {41.8130, -87.5925},
				{41.8250, -87.6020}, {41.8389, -87.6074}, {41.8510, -87.6120},
				{41.8600, -87.6110}, {41.8670, -87.6145}, {41.8740, -87.6150},
				{41.8800, -87.6150}, {41.8840, -87.6130}, {41.8890, -87.6120},
				{41.8935, -87.6115}, {41.8990, -87.6180}, {41.9030, -87.6230},
				{41.9118, -87.6250}, {41.9250, -87.6290}, {41.9330, -87.6310},
				{41.9420, -87.6355}}},
			{'p', "Lincoln Park", []LL{{41.9108, -87.6280}, {41.9108, -87.6330},
				{41.9300, -87.6400}, {41.9420, -87.6420},
				{41.9420, -87.6355}, {41.9250, -87.6290},
				{41.9118, -87.6250}}},
			{'p', "Grant Park", []LL{{41.8840, -87.6240}, {41.8675, -87.6240},
				{41.8670, -87.6145}, {41.8800, -87.6150},
				{41.8840, -87.6130}}},
			{'p', "Burnham Park", []LL{{41.8670, -87.6190}, {41.8020, -87.5900},
				{41.8020, -87.5850}, {41.8250, -87.6020},
				{41.8389, -87.6074}, {41.8510, -87.6120},
				{41.8600, -87.6110}, {41.8670, -87.6145}}},
			{'p', "Washington Park", []LL{{41.8020, -87.6180}, {41.7860, -87.6180},
				{41.7860, -87.6060}, {41.8020, -87.6060}}},
			{'p', "the Midway", []LL{{41.7880, -87.6060}, {41.7860, -87.6060},
				{41.7860, -87.5860}, {41.7880, -87.5860}}},
			{'p', "Jackson Park", []LL{{41.7910, -87.5860}, {41.7700, -87.5860},
				{41.7700, -87.5630}, {41.7790, -87.5700},
				{41.7860, -87.5750}, {41.7910, -87.5800}}},
			{'p', "Humboldt Park", []LL{{41.9070, -87.7060}, {41.8990, -87.7060},
				{41.8990, -87.6980}, {41.9070, -87.6980}}},
			{'p', "Garfield Park", []LL{{41.8860, -87.7220}, {41.8790, -87.7220},
				{41.8790, -87.7140}, {41.8860, -87.7140}}},
			{'p', "Douglass Park", []LL{{41.8640, -87.7000}, {41.8580, -87.7000},
				{41.8580, -87.6930}, {41.8640, -87.6930}}},
			{'p', "Marquette Park", []LL{{41.7730, -87.7060}, {41.7670, -87.7060},
				{41.7670, -87.6900}, {41.7730, -87.6900}}},
			{'w', "Lake Calumet", []LL{{41.6990, -87.5900}, {41.6990, -87.5700},
				{41.6650, -87.5650}, {41.6550, -87.5800},
				{41.6650, -87.5950}}},
		},
		Lines: []Shape{
			{'w', "the Chicago River", []LL{{41.8888, -87.6120}, {41.8885, -87.6200},
				{41.8882, -87.6245}, {41.8875, -87.6310},
				{41.8868, -87.6378}, {41.8920, -87.6395},
				{41.8965, -87.6420}, {41.9070, -87.6550},
				{41.9110, -87.6570}, {41.9180, -87.6650},
				{41.9300, -87.6800}, {41.9500, -87.6900}}},
			{'w', "the South Branch", []LL{{41.8868, -87.6378}, {41.8750, -87.6385},
				{41.8675, -87.6355}, {41.8560, -87.6370},
				{41.8540, -87.6450}, {41.8500, -87.6550},
				{41.8420, -87.6650}, {41.8380, -87.6750},
				{41.8330, -87.6850}, {41.8250, -87.7000},
				{41.8150, -87.7200}, {41.8050, -87.7400},
				{41.7900, -87.8100}}},
			{'w', "the Calumet River", []LL{{41.7310, -87.5280}, {41.7200, -87.5380},
				{41.7050, -87.5420}, {41.6950, -87.5550},
				{41.6900, -87.5700}}},
			{'=', "Navy Pier", []LL{{41.8917, -87.6140}, {41.8919, -87.5985}}},
			{'#', "the Loop L", []LL{{41.8857, -87.6340}, {41.8857, -87.6262},
				{41.8768, -87.6262}, {41.8768, -87.6340},
				{41.8857, -87.6340}}},
		},
		Streets: []Street{{24, "Fullrtn"}, {16, "North"}, {8, "Chicago"},
			{0, "Madison"}, {-12, "Roosvlt"}, {-22, "Cermak"},
			{-35, "35th"}, {-47, "47th"}, {-63, "63rd"},
			{-79, "79th"}, {-95, "95th"}, {-111, "111th"},
			{-130, "130th"}, {-138, "138th"}},
		Labels: []Label{{LL{41.840, -87.450}, "LAKE MICHIGAN"},
			{LL{41.640, -87.470}, "INDIANA"},
			{LL{41.700, -87.850}, "SUBURBS"}},
		Zones: map[string]Zone{"barracks": {-12, 4, 1, 14},
			"depot":  {2, 14, 4, 12},
			"far":    {8, 14, 5, 7},
			"beyond": {-99, 99, -150, -141}},
	},
}

// OtherCity is the city the c key switches to.
func OtherCity(key string) string {
	if key == "manhattan" {
		return "chicago"
	}
	return "manhattan"
}
