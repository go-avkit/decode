// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-avkit/avkit/container"
	"github.com/go-avkit/decode"
	"github.com/spf13/cobra"
)

// benchRun is one decode of one input by one side.
type benchRun struct {
	Frames  int
	Wall    time.Duration
	CPU     time.Duration // user + system
	Mallocs uint64        // ours only
	Bytes   uint64        // ours only
}

func (r benchRun) fps() float64 { return float64(r.Frames) / r.Wall.Seconds() }

// loadAvg is the machine's 1-minute load average; a variable so a test can
// drive the load gate.
var loadAvg = func() (float64, error) {
	out, err := exec.Command("uptime").Output()
	if err != nil {
		return 0, err
	}
	return parseLoad(string(out))
}

// parseLoad reads the 1-minute figure out of uptime's "load averages: a b c"
// (macOS) or "load average: a, b, c" (Linux).
func parseLoad(s string) (float64, error) {
	_, rest, ok := strings.Cut(s, "load average")
	if !ok {
		return 0, fmt.Errorf("no load average in %q", strings.TrimSpace(s))
	}
	_, rest, _ = strings.Cut(rest, ":")
	f := strings.FieldsFunc(rest, func(r rune) bool { return r == ' ' || r == ',' })
	if len(f) == 0 {
		return 0, fmt.Errorf("no load figure in %q", strings.TrimSpace(s))
	}
	return strconv.ParseFloat(f[0], 64)
}

var benchLine = regexp.MustCompile(`bench: utime=([0-9.]+)s stime=([0-9.]+)s rtime=([0-9.]+)s`)
var frameStat = regexp.MustCompile(`frame=\s*(\d+)`)

// parseFFmpegBench reads the frame count of ffmpeg's last progress line and the
// times of its -benchmark summary.
func parseFFmpegBench(stderr string) (benchRun, error) {
	m := benchLine.FindStringSubmatch(stderr)
	if m == nil {
		return benchRun{}, errors.New("no bench: line in ffmpeg's output")
	}
	all := frameStat.FindAllStringSubmatch(stderr, -1)
	if len(all) == 0 {
		return benchRun{}, errors.New("no frame= line in ffmpeg's output")
	}
	sec := func(s string) time.Duration {
		v, _ := strconv.ParseFloat(s, 64) // the regexp admits digits and dots only
		return time.Duration(v * float64(time.Second))
	}
	n, _ := strconv.Atoi(all[len(all)-1][1])
	return benchRun{Frames: n, Wall: sec(m[3]), CPU: sec(m[1]) + sec(m[2])}, nil
}

// rawDemuxer names ffmpeg's demuxer for a bare bitstream, or "" for a container.
func rawDemuxer(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".264", ".h264", ".avc", ".jvt", ".26l", ".bit":
		return "h264"
	case ".265", ".h265", ".hevc":
		return "hevc"
	}
	return ""
}

func ffmpegRun(path string) (benchRun, error) {
	args := []string{"-hide_banner", "-nostdin", "-benchmark", "-threads", "1"}
	if d := rawDemuxer(path); d != "" {
		args = append(args, "-f", d)
	}
	args = append(args, "-i", path, "-map", "0:v:0", "-fps_mode", "passthrough", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return benchRun{}, fmt.Errorf("ffmpeg: %w: %.200s", err, stderr.String())
	}
	return parseFFmpegBench(stderr.String())
}

// repeatFile writes the bitstream at path, n times over, to dir and returns the
// new path: a few-picture Annex B stream is still a valid stream when repeated
// (each copy opens with its IDR picture), and lasts long enough to be timed.
func repeatFile(path, dir string, n int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(path)
	dst := filepath.Join(dir, fmt.Sprintf("%s.x%d%s", strings.TrimSuffix(filepath.Base(path), ext), n, ext))
	return dst, os.WriteFile(dst, bytes.Repeat(data, n), 0o644)
}

// oursRun decodes the whole input with this library, the file already in
// memory, and counts the pictures it returns without keeping them.
func oursRun(path string) (benchRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return benchRun{}, err
	}
	cfg := decode.Config{MaxFrameMBs: 1 << 17}
	var step func() (int, error)
	if d := rawDemuxer(path); d != "" {
		newRaw := decode.New
		if d == "hevc" {
			newRaw = decode.NewHEVC
		}
		dec, err := newRaw(cfg, 4)
		if err != nil {
			return benchRun{}, err
		}
		defer dec.Close()
		rest := data
		step = func() (n int, err error) {
			const chunk = 1 << 20
			for len(rest) > 0 {
				c := min(chunk, len(rest))
				fs, err := dec.DecodeAnnexB(rest[:c])
				n += len(fs)
				if err != nil {
					return n, err
				}
				rest = rest[c:]
			}
			fs, err := dec.Flush()
			return n + len(fs), err
		}
	} else {
		rd, err := container.NewReader(data)
		if err != nil {
			return benchRun{}, err
		}
		id, err := firstVideo(rd)
		if err != nil {
			return benchRun{}, err
		}
		tc, err := rd.TrackConfig(id)
		if err != nil {
			return benchRun{}, err
		}
		samples, err := rd.Samples(id)
		if err != nil {
			return benchRun{}, err
		}
		dec, err := decode.NewTrack(tc, cfg)
		if err != nil {
			return benchRun{}, err
		}
		defer dec.Close()
		step = func() (n int, err error) {
			var dts int64
			for _, s := range samples {
				fs, err := dec.DecodeSample(s.Data, dts+int64(s.CompositionOffset))
				n += len(fs)
				if err != nil {
					return n, err
				}
				dts += int64(s.Duration)
			}
			fs, err := dec.Flush()
			return n + len(fs), err
		}
	}
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	c0, t0 := cpuTime(), time.Now()
	n, err := step()
	wall, cpu := time.Since(t0), cpuTime()-c0
	runtime.ReadMemStats(&m1)
	return benchRun{Frames: n, Wall: wall, CPU: cpu, Mallocs: m1.Mallocs - m0.Mallocs, Bytes: m1.TotalAlloc - m0.TotalAlloc}, err
}

func firstVideo(rd *container.Reader) (uint32, error) {
	for _, t := range rd.File().Tracks {
		if t.Kind == container.Video {
			return t.ID, nil
		}
	}
	return 0, errors.New("no video track")
}

// median and extremes of a non-empty slice.
func median(v []float64) float64 {
	s := slices.Sorted(slices.Values(v))
	if n := len(s); n%2 == 0 {
		return (s[n/2-1] + s[n/2]) / 2
	}
	return s[len(s)/2]
}

// benchRow accumulates the rounds of one input.
type benchRow struct {
	Name         string
	ours, ref    []float64 // frames per second
	oursCPU      []float64 // CPU-seconds per frame
	refCPU       []float64
	frames       int
	refFrames    int
	allocs, kibs float64 // per frame, exact, from the last round
}

func fpsCell(v []float64) string {
	if len(v) == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f (%.1f-%.1f)", median(v), slices.Min(v), slices.Max(v))
}

// benchTable renders the markdown table: frames per second as median and range
// over the rounds, the ratio of the medians, and our allocations per picture.
func benchTable(rows []*benchRow) string {
	var b strings.Builder
	b.WriteString("| input | frames ours/ffmpeg | rounds | ours fps med (min-max) | ffmpeg fps med (min-max) | ratio ours/ffmpeg | CPU ratio | allocs/frame | KiB/frame |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rows {
		ratio, cpu := "-", "-"
		if len(r.ours) > 0 && len(r.ref) > 0 {
			ratio = fmt.Sprintf("%.2f", median(r.ours)/median(r.ref))
			cpu = fmt.Sprintf("%.2f", median(r.refCPU)/median(r.oursCPU))
		}
		fmt.Fprintf(&b, "| %s | %d/%d | %d/%d | %s | %s | %s | %s | %.1f | %.0f |\n", r.Name, r.frames, r.refFrames,
			len(r.ours), len(r.ref), fpsCell(r.ours), fpsCell(r.ref), ratio, cpu, r.allocs, r.kibs)
	}
	return b.String()
}

// runBench interleaves ours and ffmpeg over every input for up to rounds rounds,
// and stops, keeping what it has, when the 1-minute load reaches maxLoad. It
// returns the rows, the loads seen and whether it stopped early.
func runBench(inputs []string, rounds, repeat int, workdir string, maxLoad float64, log io.Writer) (rows []*benchRow, loads []float64, stopped bool, err error) {
	inputs = slices.Clone(inputs)
	for i, p := range inputs {
		name := filepath.Base(p)
		if repeat > 1 && rawDemuxer(p) != "" {
			if inputs[i], err = repeatFile(p, workdir, repeat); err != nil {
				return nil, nil, false, err
			}
			name += fmt.Sprintf(" x%d", repeat)
		}
		rows = append(rows, &benchRow{Name: name})
	}
	for round := 1; round <= rounds; round++ {
		for i, p := range inputs {
			load, err := loadAvg()
			if err != nil {
				return rows, loads, false, err
			}
			loads = append(loads, load)
			if load >= maxLoad {
				fmt.Fprintf(log, "round %d, %s: load %.2f >= %.1f, stopping\n", round, rows[i].Name, load, maxLoad)
				return rows, loads, true, nil
			}
			r := rows[i]
			o, err := oursRun(p)
			if err != nil {
				return rows, loads, false, fmt.Errorf("%s: ours: %w", r.Name, err)
			}
			f, err := ffmpegRun(p)
			if err != nil {
				return rows, loads, false, fmt.Errorf("%s: %w", r.Name, err)
			}
			r.ours, r.ref = append(r.ours, o.fps()), append(r.ref, f.fps())
			r.oursCPU = append(r.oursCPU, o.CPU.Seconds()/float64(o.Frames))
			r.refCPU = append(r.refCPU, f.CPU.Seconds()/float64(f.Frames))
			r.frames, r.refFrames = o.Frames, f.Frames
			r.allocs, r.kibs = float64(o.Mallocs)/float64(o.Frames), float64(o.Bytes)/float64(o.Frames)/1024
			fmt.Fprintf(log, "round %d %s load %.2f: ours %.1f fps, ffmpeg %.1f fps\n", round, r.Name, load, o.fps(), f.fps())
		}
	}
	return rows, loads, false, nil
}

// pause is how long waitQuiet sleeps between looks at the load; a test shortens it.
var pause = 15 * time.Second

// waitQuiet returns once the load is below maxLoad, or when wait has passed
// (the first round then stops on its own load check).
func waitQuiet(wait time.Duration, maxLoad float64, log io.Writer) error {
	for deadline := time.Now().Add(wait); time.Now().Before(deadline); time.Sleep(pause) {
		load, err := loadAvg()
		if err != nil {
			return err
		}
		if load < maxLoad {
			return nil
		}
		fmt.Fprintf(log, "waiting: load %.2f >= %.1f\n", load, maxLoad)
	}
	return nil
}

func benchCmd() *cobra.Command {
	var rounds, repeat int
	var workdir, cpuprofile string
	var maxLoad float64
	var wait time.Duration
	c := &cobra.Command{
		Use:   "bench PATH...",
		Short: "decode speed against ffmpeg, one core each, interleaved rounds; stops when the load reaches --max-load",
		Long: "bench decodes each PATH (a .264/.265 bitstream, or an MP4/MKV file) with this library at GOMAXPROCS=1\n" +
			"and with `ffmpeg -threads 1 -benchmark -f null -`, alternating the two, and prints a markdown table:\n" +
			"frames per second (median and range), the ratio, and our exact allocations per picture.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if rounds < 1 || repeat < 1 {
				return errors.New("--rounds and --repeat must be at least 1")
			}
			if repeat > 1 && workdir == "" {
				return errors.New("--repeat needs --workdir")
			}
			if err := waitQuiet(wait, maxLoad, cmd.ErrOrStderr()); err != nil {
				return err
			}
			defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
			if cpuprofile != "" {
				f, err := os.Create(cpuprofile)
				if err != nil {
					return err
				}
				defer f.Close()
				if err := pprof.StartCPUProfile(f); err != nil {
					return err
				}
				defer pprof.StopCPUProfile()
			}
			rows, loads, stopped, err := runBench(args, rounds, repeat, workdir, maxLoad, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprint(out, benchTable(rows))
			if len(loads) > 0 {
				fmt.Fprintf(out, "\n1-minute load before each run: min %.2f, median %.2f, max %.2f (%d samples)\n",
					slices.Min(loads), median(loads), slices.Max(loads), len(loads))
			}
			if stopped {
				fmt.Fprintf(out, "STOPPED EARLY: the load reached %.1f; the rows hold the rounds completed before.\n", maxLoad)
			}
			return nil
		},
	}
	c.Flags().DurationVar(&wait, "wait", 0, "before the first round, wait up to this long for the load to fall below --max-load")
	c.Flags().StringVar(&cpuprofile, "cpuprofile", "", "write a CPU profile of our side (ffmpeg is a child process and is not in it) to this file")
	c.Flags().IntVar(&rounds, "rounds", 5, "interleaved rounds")
	c.Flags().IntVar(&repeat, "repeat", 1, "concatenate each bare bitstream this many times, for streams of a few pictures (written to --workdir)")
	c.Flags().StringVar(&workdir, "workdir", "", "durable directory for the repeated bitstreams (required with --repeat > 1)")
	c.Flags().Float64Var(&maxLoad, "max-load", 8, "stop when the 1-minute load average reaches this")
	return c
}
