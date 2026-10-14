# AudioSmith's patches to MyGo

AudioSmith builds against `third_party/mygo` (the `replace` in `go.mod`): MyGo at the version `go.mod` requires, with these patches, made by `scripts/vendor-mygo.sh` from the module cache. The copy leaves out MyGo's website, docs, examples, CI, tests and test data; `scripts/vendor-mygo.sh --check` tells whether it is exactly what the script makes. Never edit `third_party/mygo` by hand: change a patch and run the script.

Keep each patch small and send it upstream (egoist/mygo); drop it when a release has the fix. With no patch left, delete `third_party/mygo` and the replace (`go mod edit -dropreplace github.com/egoist/mygo`).

| Patch | Why |
|---|---|
| `0001-coretext-own-width.patch` | On macOS a single-line text given exactly its measured width ended in "…" ("M4B（含章节）" in the audiobook's Format control; 177 zh-Hans and 181 English strings of the catalogue at some size), and wrapping text in a box of its own width dropped its last word to a second line. Core Text spaces a bracket apart from a letter of another script; between runs of two fonts it only moves glyphs, so the sum of advances MyGo takes as the width came out short of what `CTTypesetterSuggestLineBreak` needs. Long lines also lost more than Core Text forgives (0.0002 DIPs) to float32 sums. The patch folds run gaps into advances, sums widths in float64 and breaks at width × (1 + 1e-6). The patch header explains it in full. |

Make a patch: copy the module (`go env GOMODCACHE`/github.com/egoist/mygo@VERSION) somewhere writable, `git init` and commit it, change it, run its tests on the Mac and Linux, `git diff > third_party/mygo-patches/000N-name.patch`, then run `scripts/vendor-mygo.sh`.
