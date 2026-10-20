#!/usr/bin/env python3
"""curate-report.py DIR [picks.json]: the curated voices' measurements
(thread #94), from make-curated-voices.sh's pulled results in DIR.

For every designed voice and candidate seed: the kept reference (refcheck
grade, length, character error rate of what Qwen3-ASR heard) and the Base
clones of the test lines (their error rates; UTMOS22 when DIR/mos.json
has it, a sort aid only). A seed is usable when its reference says its
text (CER <= 5 %, grade good or ok) and both clones do (CER <= 5 %); the
suggested pick is the usable seed with the best UTMOS22 of its clones,
then the lowest clone error, then the better grade. Nobody listened: the
picks are for the user's ears.

Writes DIR/report.md and, without picks.json, DIR/picks.json (the
suggestions); prints the table.
"""
import json
import os
import sys
import unicodedata


def norm(s):
    s = unicodedata.normalize("NFKC", s).lower()
    return "".join(c for c in s if unicodedata.category(c)[0] in "LN")


def cer(want, heard):
    a, b = norm(want), norm(heard)
    if not a:
        return 0.0 if not b else 1.0
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1] / len(a)


def main():
    out = sys.argv[1]
    asr = json.load(open(os.path.join(out, "asr.json"))) if os.path.exists(os.path.join(out, "asr.json")) else {}
    lines = json.load(open(os.path.join(out, "lines.json")))
    mp = os.path.join(out, "mos.json")
    mos = json.load(open(mp)) if os.path.exists(mp) else {}
    rows, picks = [], {}
    for vid in sorted(d for d in os.listdir(out) if d.startswith("c-")):
        seeds = sorted(d for d in os.listdir(os.path.join(out, vid)) if d.startswith("s"))
        best = None
        for s in seeds:
            p = os.path.join(out, vid, s, "ref.json")
            if not os.path.exists(p):
                continue
            r = json.load(open(p))
            lang = r.get("lang") or ""
            c = r["cands"][r["best"]] if r.get("best", -1) >= 0 else None
            row = {"id": vid, "seed": s, "ok": r.get("ok"), "grade": r.get("grade"), "refCER": c["cer"] if c else None,
                   "refSecs": c["secs"] if c else None, "text": r.get("text", "")}
            ccer = []
            for ln in "NC":
                heard = asr.get(f"{vid}/{s}/{ln}.wav")
                want = lines.get(vid, {}).get(ln)
                if heard is not None and want:
                    ccer.append(cer(want, heard))
            row["cloneCER"] = ccer
            ms = [mos[f"{vid}/{s}/{ln}.wav"] for ln in "NC" if f"{vid}/{s}/{ln}.wav" in mos]
            row["mos"] = sum(ms) / len(ms) if ms else None
            row["usable"] = bool(r.get("ok")) and row["grade"] in ("good", "ok") and c is not None and 0 <= c["cer"] <= 0.05 \
                and len(ccer) == 2 and max(ccer) <= 0.05
            rows.append(row)
            if row["usable"]:
                key = (-(row["mos"] or 0), sum(ccer), 0 if row["grade"] == "good" else 1)
                if best is None or key < best[0]:
                    best = (key, int(s[1:]))
        if best:
            picks[vid[2:]] = best[1]
    md = ["| voice | seed | ref ok | grade | ref CER | ref s | clone CER N / C | UTMOS22 | usable |", "|---|---|---|---|---|---|---|---|---|"]
    for r in rows:
        cc = " / ".join(f"{x:.1%}" for x in r["cloneCER"]) or "–"
        rc = f"{r['refCER']:.1%}" if r["refCER"] is not None and r["refCER"] >= 0 else "–"
        rs = f"{r['refSecs']:.1f}" if r["refSecs"] else "–"
        mark = "**pick**" if picks.get(r["id"][2:]) == int(r["seed"][1:]) else ("yes" if r["usable"] else "no")
        mo = f"{r['mos']:.2f}" if r["mos"] is not None else "–"
        md.append(f"| {r['id']} | {r['seed']} | {r['ok']} | {r['grade']} | {rc} | {rs} | {cc} | {mo} | {mark} |")
    text = "\n".join(md) + "\n"
    open(os.path.join(out, "report.md"), "w").write(text)
    if len(sys.argv) < 3:
        json.dump(picks, open(os.path.join(out, "picks.json"), "w"), indent=1)
    print(text)
    print(f"{len(picks)} voices with a usable seed")


main()
