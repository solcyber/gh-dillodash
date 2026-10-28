# Fortress mode (`d`)

`d` in the list draws everything open as a Dwarf Fortress map over
**Manhattan** or **Chicago** (a coin flip on each `d`; `c` swaps). It is a
joke with a fixed encoding, so you can read it:

- **Row = age.** The numbered street is the age in days since the thing was
  opened (branches and commits: since their last commit). In Manhattan 14th
  St is 14 days old, Houston St is today and older is north; past 220th St
  is "the Bronx". In Chicago 35th St is 35 days old, Madison is today and
  older is south; past 138th St is "Indiana".
- **Lane = repo.** The city's land at each row is cut west to east into one
  lane per repo, in config order. The legend lists the order.
- **Shape = what it is; color and motion = its state:**

  | Glyph (unicode · ascii) | Thing | State |
  |---|---|---|
  | `@` dwarf (a smiley with the Nerd Font) | open PR | yellow, blinking `!`: awaiting review · light red, flickering fire: CI failing · cyan: approved · magenta: changes requested · white: no review asked · walks: CI running |
  | `ø` · `o` | draft PR | gray |
  | `g` goblin, `T` troll | issue, epic issue | yellow: assigned to you · red: a bug, incident or security label |
  | `░▒▓` · `.:#` ruins | branches with no open PR | denser shade = more branches in that cell (a stale-branch heatmap) |
  | `Ω` · `^` statue | commits on the default branch (newest 30 per repo) | white and glinting: under 24 hours old |
  | `$` merchant | release PR into production, at the **depot** (the Seaport / Navy Pier) | yellow: needs approval · cyan: approved · blue, across the bridge or out on the pier: merged in the last 12 hours (`release_pr`) |
  | `■` / `□` · `W` / `w` wagon | staging units ahead of production, at the depot | yellow `■`: go · gray `□`: hold or untagged (`release_pr`) |
  | `z` sleeper | queued for the maintenance window, in the **barracks** (Battery Park / River North) | yellow: attended night needed · gray: rolled (`maintenance_window`) |

The **look cursor** names whatever is under it in the look panel: the repo
and number, state, title, age. `hjkl`/arrows move it (`HJKL` by 8),
`tab`/shift-`tab` jump between things in order of urgency, `enter` opens a
PR or issue in the ordinary detail view (`q` there comes back to the map),
`y`/`o` copy or open the thing's URL, including a branch or commit.

The whole city fits the screen at a few days per row, widened so each lane
has room. `+` zooms to one street per day at true aspect and the view
follows the cursor; `-` fits it again. Anything crowded out of the fit view
is counted in the legend. The top left logs what each refresh changed, DF
style ("Migrants have arrived: infra#40").

**What it costs.** The map draws only what the last refresh fetched. PRs
and issues come from the list's queries; branches and commits come from one
aliased query per refresh that joins the refresh only after someone pressed
`d` that session.

**Colors.** This view uses Dwarf Fortress's 16 colors, including its red
and green, so it is the exception to the colorblind-safe palette. Every
state also has its own shape or motion. With `--motion off`, `--mono` or
`NO_COLOR`, the status glyph itself replaces the dwarf, so shape alone
carries every state. Text on the map keeps the ordinary palette.

Animation runs at 4 frames a second, slowing to 1 after 5 minutes without
a key, so the map can be left up as a wall dashboard. It needs a terminal
of at least 60x20.
