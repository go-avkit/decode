VP8 and VP9 test streams, made with ffmpeg (libvpx, libvpx-vp9; `-threads 1 -an`) from lavfi sources:

| file | source | encoder options |
|---|---|---|
| vp8_altref.webm | mandelbrot 176x144 15 fps, 30 frames | libvpx -b:v 300k -auto-alt-ref 1 -lag-in-frames 16 -g 15 |
| vp8_odd.webm | testsrc2 161x97, 20 frames | libvpx -crf 10 -b:v 0 |
| vp9_altref.webm | mandelbrot 176x144, 30 frames | libvpx-vp9 -b:v 300k -auto-alt-ref 1 -lag-in-frames 16 -g 15 -deadline good -cpu-used 4 |
| vp9_odd.webm | testsrc2 161x97, 20 frames | libvpx-vp9 -crf 25 -b:v 0 -deadline good -cpu-used 4 |
| vp9_tiles.webm | mandelbrot 640x96, 10 frames | libvpx-vp9 -b:v 400k -tile-columns 1 -deadline good -cpu-used 4 |
| vp9_profile1.webm | testsrc2 64x64 yuv444p, 5 frames | libvpx-vp9 -profile:v 1 (refused: profile 1) |
| vp9_profile2.webm | testsrc2 64x64 yuv420p10le, 5 frames | libvpx-vp9 -profile:v 2 (refused: profile 2) |

All are `-pix_fmt yuv420p` (libvpx-vp9 picks profile 1 for a 4:4:4 source); the odd-sized ones are
`testsrc2=size=162x98` cut with `-vf crop=161:97:exact=1`.

Each `.md5` is `ffmpeg -threads 1 -i X.webm -f framemd5 -pix_fmt yuv420p X.md5`: ffmpeg's own decoder.
