#!/usr/bin/env bash
# Turns a take (story.mjs) into the README GIF and an MP4 of the same cut at full size. The
# timeline marks stretches: "normal" plays at 1x, "fast" (waiting on an agent) is sped up to at
# most DEMO_FAST_SECONDS with a badge, "cut" is dropped.
#
# Usage: scripts/demo-video/encode.sh <raw.mkv> <timeline.json> <out.gif> [out.mp4]
# Env: DEMO_WIDTH (GIF width, default 1100), DEMO_FPS (GIF frame rate, default 10),
# DEMO_FAST_SECONDS (default 3), FFMPEG (default ffmpeg).
set -euo pipefail

raw=${1:?raw video}
timeline=${2:?timeline}
gif=${3:?output gif}
mp4=${4:-${gif%.gif}.mp4}
ffmpeg=${FFMPEG:-ffmpeg}
ffprobe=${ffmpeg%ffmpeg}ffprobe
width=${DEMO_WIDTH:-1100}
fps=${DEMO_FPS:-10}
fast=${DEMO_FAST_SECONDS:-3}
font=$(fc-match -f '%{file}' 'DejaVu Sans:bold')
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The cut, as a filter graph ending in [cat]: every kept stretch, sped up where marked.
python3 - "$timeline" "$fast" "$font" >"$work/cut" <<'EOF'
import json, sys
marks = json.load(open(sys.argv[1]))
fast, font = float(sys.argv[2]), sys.argv[3]
segments = []
for a, b in zip(marks, marks[1:]):
    if a["mode"] != "cut" and b["t"] - a["t"] > 0.05:
        segments.append((a["t"], b["t"], a["mode"]))
parts = [f"[0:v]split={len(segments)}" + "".join(f"[s{i}]" for i in range(len(segments)))]
for i, (start, end, mode) in enumerate(segments):
    chain = f"[s{i}]trim=start={start:.3f}:end={end:.3f},setpts=PTS-STARTPTS"
    if mode == "fast":
        speed = max(4.0, (end - start) / fast)
        chain += (f",setpts=PTS/{speed:.3f},drawtext=fontfile='{font}':text='>> {speed:.0f}x':fontsize=26"
                  ":fontcolor=white:box=1:boxcolor=0x0d9488@0.9:boxborderw=10:x=w-tw-28:y=h-th-28")
    parts.append(chain + f"[v{i}]")
parts.append("".join(f"[v{i}]" for i in range(len(segments))) + f"concat=n={len(segments)}:v=1:a=0[cat]")
print(";\n".join(parts), end="")
EOF

{ cat "$work/cut"; echo ";"
  echo "[cat]fps=$fps,scale=$width:-1:flags=lanczos,split[a][b];"
  echo "[a]palettegen=max_colors=192:stats_mode=diff[p];"
  echo "[b][p]paletteuse=dither=bayer:bayer_scale=4:diff_mode=rectangle"; } >"$work/gif"
{ cat "$work/cut"; echo ";[cat]fps=25,format=yuv420p"; } >"$work/mp4"

"$ffmpeg" -v error -y -i "$raw" -filter_complex_script "$work/gif" -loop 0 "$gif"
"$ffmpeg" -v error -y -i "$raw" -filter_complex_script "$work/mp4" -c:v libx264 -crf 20 -preset slow \
  -movflags +faststart "$mp4"
for f in "$gif" "$mp4"; do
  echo "wrote $f ($(du -h "$f" | cut -f1), $("$ffprobe" -v error -show_entries format=duration -of csv=p=0 "$f")s)"
done
