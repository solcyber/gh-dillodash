# Hack Nerd Font Mono: the dashboard's terminal font

The dashboard's `nerd` icon set draws from the [Nerd Fonts](https://www.nerdfonts.com)
Octicons glyphs, which is GitHub's own icon set. These two files are
**Hack Nerd Font Mono** Regular and Bold from Nerd Fonts **v3.5.1**. They are
embedded in the binary (`go:embed`), so `gh dillodash font install` writes a
known, checksummed font with no network access and no extra download.

| File | sha256 |
|---|---|
| `HackNerdFontMono-Regular.ttf` | `e5aeb58577a8acf2b42083e4a07ce48bd53116fdd868636403cadb5b547351be` |
| `HackNerdFontMono-Bold.ttf` | `7c2c76c26ff80fb546798b204ae519335573417c969eb3142d202298722d3832` |

**Source archive:** `https://github.com/ryanoasis/nerd-fonts/releases/download/v3.5.1/Hack.tar.xz`,
sha256 `cdd389472e10e2261520140ff1b382b4f8a226af5fd0b2735b975d31151d9c3c`, as
listed in that release's `SHA-256.txt`.

**Only the two `Mono` styles are kept.** "Mono" makes every icon exactly one
cell wide, so the dashboard's columns line up. The plain and `Propo` variants
draw some icons wider, and those spill into the next column. The checksums
above are Go constants (`RegularSHA256`, `BoldSHA256` in `fonts.go`); the
installer refuses to write a file whose embedded bytes do not hash to them,
and the package tests check both the hashes and that every glyph the `nerd`
and `unicode` sets draw exists in both styles, one cell wide.

**Why the files are embedded rather than downloaded at install:** an offline
install, one less network dependency, and a font that is always the one the
glyph tests were run against.

**Licenses:**
- Hack: MIT, plus the Bitstream Vera License for its Vera base (`LICENSE.md`).
- The Nerd Fonts patcher and patched fonts: `NERD-FONTS-LICENSE.md`.
- The per-icon-set licenses (Octicons is MIT) are in the table in
  `NERD-FONTS-README.md`, which is the upstream archive's README, unchanged.

## Re-pinning to a newer release

1. Download the new release's archive and checksums, then check
   `sha256sum Hack.tar.xz` against `SHA-256.txt`:
   ```sh
   gh release download vX.Y.Z --repo ryanoasis/nerd-fonts --pattern Hack.tar.xz --pattern SHA-256.txt
   ```
2. Extract `HackNerdFontMono-Regular.ttf`, `HackNerdFontMono-Bold.ttf`,
   `LICENSE.md` and `README.md` into this directory. Save the README as
   `NERD-FONTS-README.md`.
3. Update `Version`, `RegularSHA256` and `BoldSHA256` in `fonts.go`
   (`sha256sum HackNerdFontMono-*.ttf`).
4. Run `go test ./internal/fonts/`. It fails if any glyph the dashboard draws
   is missing from the new build, or if a glyph is not one cell wide.
5. Update the version, hashes and source archive on this page.
