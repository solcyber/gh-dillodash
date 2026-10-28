package fortress

import "math"

// Zoom levels: fit squeezes the whole city into the view (one row is
// several days); street is one street (one day) per row.
const (
	ZoomFit    = "fit"
	ZoomStreet = "street"
)

// Cell is a map cell: row (north to south) and column.
type Cell struct{ R, C int }

// Span is the columns [A, B) of the city proper on a row; OK is false
// when the row has none.
type Span struct {
	A, B int
	OK   bool
}

// MapLabel is a caption placed on the raster.
type MapLabel struct {
	Row, Col int
	Text     string
}

// Map is a rasterized city: terrain codes per cell, the name of what
// each cell is, which cells are the city proper (lanes, placement), and
// the cell <-> grid-unit scale. Rows run north to south.
type Map struct {
	Key  string
	City *City
	Zoom string

	Rows, Cols int
	// T is the terrain code of each cell; Main marks the city proper.
	T    [][]byte
	Main [][]bool
	name [][]int
	// Names is what each cell is (by index in name).
	Names []string
	// StreetRows maps a row to the street label drawn in the margin.
	StreetRows map[int]string
	Labels     []MapLabel
	// Span is each row's span of the city proper, for the lanes.
	Span []Span

	spr, cpu, xfac float64 // units per row, columns per unit, lane widening
	ytop, xleft    float64
}

// NewMap rasterizes a city into h rows by at least w columns. zoom is
// ZoomFit or ZoomStreet; lanes widens the city so every lane has room;
// cx (when > 0) is the column the city's middle sits at (left of the
// legend), else the raster's middle.
func NewMap(key string, w, h int, zoom string, lanes int, cx float64) *Map {
	city := Cities[key]
	if city == nil {
		city = Cities[CityKeys[0]]
	}
	proj := city.Projected()
	m := &Map{Key: city.Key, City: city, Zoom: zoom}
	y0, y1 := city.Y0, city.Y1
	mx0, mx1 := proj.MainX0, proj.MainX1
	var cpu float64
	var rows int
	if zoom == ZoomFit {
		m.spr = (y1 - y0) / float64(max(1, h)) // units per row
		cpu = 2.0 / m.spr                     // cols per unit
		// widen (not to scale) so each lane gets room for its glyphs
		need := float64(max(lanes*6, 24)) / ((mx1 - mx0) * 0.7 * cpu)
		m.xfac = math.Min(3.0, math.Max(1.0, need))
		cpu *= m.xfac
		rows = h
	} else {
		m.spr, cpu, m.xfac = 1.0, 2.0, 1.0
		rows = int(y1 - y0)
	}
	m.cpu = cpu
	cols := max(w, int((mx1-mx0)*cpu)+24)
	m.Rows, m.Cols = rows, cols
	m.ytop = y1
	center := float64(cols) / 2.0
	if cx > 0 {
		center = cx
	}
	m.xleft = (mx0+mx1)/2.0 - center/cpu
	m.T = make([][]byte, rows)
	m.name = make([][]int, rows)
	m.Main = make([][]bool, rows)
	for r := range m.T {
		m.T[r] = make([]byte, cols)
		for c := range m.T[r] {
			m.T[r][c] = city.Base
		}
		m.name[r] = make([]int, cols)
		m.Main[r] = make([]bool, cols)
	}
	switch {
	case city.Base == 'w' && city.Key == "chicago":
		m.Names = []string{"Lake Michigan"}
	case city.Base == 'w':
		m.Names = []string{"the harbor"}
	default:
		m.Names = []string{"the suburbs"}
	}
	for _, s := range proj.Shapes {
		m.Names = append(m.Names, s.Name)
		idx := len(m.Names) - 1
		for r := 0; r < rows; r++ {
			y := m.RowY(r)
			for _, sp := range scanSpans(s.Pts, y) {
				c0 := max(0, int(math.RoundToEven((sp[0]-m.xleft)*cpu)))
				c1 := min(cols, int(math.RoundToEven((sp[1]-m.xleft)*cpu)))
				for c := c0; c < c1; c++ {
					m.T[r][c] = s.Code
					m.name[r][c] = idx
					if s.Code == 'L' {
						m.Main[r][c] = true
					} else if s.Code == 'w' && m.Main[r][c] {
						m.Main[r][c] = false
					}
				}
			}
		}
	}
	for _, l := range proj.Lines {
		m.Names = append(m.Names, l.Name)
		idx := len(m.Names) - 1
		for i := 0; i+1 < len(l.Pts); i++ {
			a, b := l.Pts[i], l.Pts[i+1]
			steps := int(math.Max(math.Max(math.Abs(b.X-a.X)*cpu, math.Abs(b.Y-a.Y)/m.spr), 1)*2) + 1
			for k := 0; k <= steps; k++ {
				x := a.X + (b.X-a.X)*float64(k)/float64(steps)
				y := a.Y + (b.Y-a.Y)*float64(k)/float64(steps)
				r, c := m.CellOf(x, y)
				if r < 0 || r >= rows || c < 0 || c >= cols {
					continue
				}
				if l.Code == 'B' && m.T[r][c] != 'L' && m.T[r][c] != 'p' {
					continue // a road only on land
				}
				m.T[r][c] = l.Code
				m.name[r][c] = idx
				if l.Code == 'w' {
					m.Main[r][c] = false
				}
			}
		}
	}
	// a row per labelled street, across the city proper
	m.StreetRows = map[int]string{}
	for _, st := range city.Streets {
		r, _ := m.CellOf(0, float64(st.Y))
		if r < 0 || r >= rows {
			continue
		}
		m.StreetRows[r] = st.Label
		if zoom == ZoomFit && m.spr > 2 {
			continue // too coarse to draw the street itself
		}
		for c := 0; c < cols; c++ {
			if m.Main[r][c] && m.T[r][c] == 'L' {
				m.T[r][c] = '-'
			}
		}
	}
	for _, l := range proj.Labels {
		r, c := m.CellOf(l.At.X, l.At.Y)
		if r >= 0 && r < rows {
			m.Labels = append(m.Labels, MapLabel{r, c - len(l.Text)/2, l.Text})
		}
	}
	// each row's span of the city proper, for the lanes
	m.Span = make([]Span, rows)
	for r := 0; r < rows; r++ {
		first, last := -1, -1
		for c := 0; c < cols; c++ {
			if m.Main[r][c] {
				if first < 0 {
					first = c
				}
				last = c
			}
		}
		if first >= 0 {
			m.Span[r] = Span{first, last + 1, true}
		}
	}
	return m
}

// UnitsPerRow is how many grid units (days) one row covers.
func (m *Map) UnitsPerRow() float64 { return m.spr }

// RowY is the grid y at the middle of row r.
func (m *Map) RowY(r int) float64 { return m.ytop - (float64(r)+0.5)*m.spr }

// ColX is the grid x at the middle of column c.
func (m *Map) ColX(c int) float64 { return m.xleft + (float64(c)+0.5)/m.cpu }

// CellOf is the (row, col) of a grid point.
func (m *Map) CellOf(x, y float64) (int, int) {
	return int((m.ytop - y) / m.spr), int((x - m.xleft) * m.cpu)
}

// AgeRow is the row of an item age days old (street = days).
func (m *Map) AgeRow(age float64) int {
	r, _ := m.CellOf(0, age*float64(m.City.Dir))
	return r
}

// ZoneCells lists the cells of a zone's box that items can stand on,
// row-major from the box's end nearest the city.
func (m *Map) ZoneCells(zone string) []Cell {
	z, ok := m.City.Zones[zone]
	if !ok {
		return nil
	}
	var out []Cell
	for r := 0; r < m.Rows; r++ {
		y := m.RowY(r)
		if y < z.Y0 || y > z.Y1 {
			continue
		}
		for c := 0; c < m.Cols; c++ {
			x := m.ColX(c)
			if x < z.X0 || x > z.X1 {
				continue
			}
			t := m.T[r][c]
			var ok bool
			switch {
			case zone == "far":
				ok = t == 'o' || t == '='
			case zone == "beyond":
				ok = t == 'o' && !m.Main[r][c]
			case zone == "depot" && m.Key == "chicago":
				ok = t == '=' || (m.Main[r][c] && in(t, "Lp-B"))
			default:
				ok = m.Main[r][c] && in(t, "Lp-B#")
			}
			if ok {
				out = append(out, Cell{r, c})
			}
		}
	}
	return out
}

func in(t byte, set string) bool {
	for i := 0; i < len(set); i++ {
		if set[i] == t {
			return true
		}
	}
	return false
}

// Placeable says whether an item may stand on (r, c): the city proper,
// on land, a park, a street, a road or the rail.
func (m *Map) Placeable(r, c int) bool {
	return r >= 0 && r < m.Rows && c >= 0 && c < m.Cols && m.Main[r][c] && in(m.T[r][c], "Lp-B#")
}

// What names the cell (the neighborhood, the river, the park), or "".
func (m *Map) What(r, c int) string {
	if r < 0 || r >= m.Rows || c < 0 || c >= m.Cols {
		return ""
	}
	return m.Names[m.name[r][c]]
}

// IsMain reports whether (r, c) is the city proper.
func (m *Map) IsMain(r, c int) bool {
	return r >= 0 && r < m.Rows && c >= 0 && c < m.Cols && m.Main[r][c]
}
