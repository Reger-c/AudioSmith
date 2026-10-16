# AudioSmith's patches to MyGo

AudioSmith builds against `third_party/mygo` (the `replace` in `go.mod`): MyGo at the version `go.mod` requires, with these patches, made by `scripts/vendor-mygo.sh` from the module cache. The copy leaves out MyGo's website, docs, examples, CI, tests and test data; `scripts/vendor-mygo.sh --check` tells whether it is exactly what the script makes. Never edit `third_party/mygo` by hand: change a patch and run the script.

Keep each patch small and send it upstream (egoist/mygo); drop it when a release has the fix. With no patch left, delete `third_party/mygo` and the replace (`go mod edit -dropreplace github.com/egoist/mygo`).

| Patch | Why |
|---|---|
| `0001-coretext-own-width.patch` | On macOS a long single-line text given exactly its measured width ended in "…", and wrapping text in a box of its own width dropped its last word to a second line: `CTTypesetterSuggestLineBreak` forgives a width at most 0.0002 DIPs short of its own, and MyGo sums a line's width in float32, which loses more on long lines. The patch sums widths in float64 and breaks at width × (1 + 1e-6). MyGo 0.5 fixed the other half of the original patch itself (`penAdvances` counts the room Core Text leaves between runs of two fonts, as in "M4B（含章节）"); on 0.5.5 without the patch, its test still fails 10 cases on macOS 26.7 (long lines, 20-point text). The patch header explains it in full. |
| `0002-select-list-scrolls.patch` | `ui.Select`'s list had no height limit and no scroll: a long one (Kokoro's 25 voices, the gallery's categories) ran off the bottom of the window, its last options out of reach. The list now scrolls in the room left below the trigger (at most 20 rows' worth); the highlighted option already scrolled into view. |

Make a patch: copy the module (`go env GOMODCACHE`/github.com/egoist/mygo@VERSION) somewhere writable, `git init` and commit it, change it, run its tests on the Mac and Linux, `git diff > third_party/mygo-patches/000N-name.patch`, then run `scripts/vendor-mygo.sh`.
