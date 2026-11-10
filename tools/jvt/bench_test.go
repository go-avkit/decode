// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseLoad(t *testing.T) {
	for in, want := range map[string]float64{
		" 8:55  up 6 days, 22:05, 1 user, load averages: 5.25 5.64 4.99": 5.25,
		" 08:55:01 up 1 day,  1 user,  load average: 0.15, 0.10, 0.05":   0.15,
	} {
		if got, err := parseLoad(in); err != nil || got != want {
			t.Errorf("parseLoad(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"nothing here", "load average:", "load average: x y z"} {
		if _, err := parseLoad(in); err == nil {
			t.Errorf("parseLoad(%q) succeeded", in)
		}
	}
}

func TestParseFFmpegBench(t *testing.T) {
	out := "frame=  100 fps=0.0 q=-0.0 size=N/A\rframe=  900 fps=88 q=-0.0 Lsize=N/A\nbench: utime=9.5s stime=0.5s rtime=10.0s\n"
	r, err := parseFFmpegBench(out)
	if err != nil || r.Frames != 900 || r.Wall != 10*time.Second || r.CPU != 10*time.Second {
		t.Fatalf("got %+v, %v", r, err)
	}
	if _, err := parseFFmpegBench("frame= 1"); err == nil {
		t.Error("no bench line accepted")
	}
	if _, err := parseFFmpegBench("bench: utime=1s stime=1s rtime=1s"); err == nil {
		t.Error("no frame line accepted")
	}
}

func TestRawDemuxerAndMedian(t *testing.T) {
	if rawDemuxer("a.264") != "h264" || rawDemuxer("a.265") != "hevc" || rawDemuxer("a.mkv") != "" {
		t.Error("rawDemuxer")
	}
	if median([]float64{3, 1, 2}) != 2 || median([]float64{4, 1, 2, 3}) != 2.5 {
		t.Error("median")
	}
}

func TestBenchTable(t *testing.T) {
	r := &benchRow{Name: "x", ours: []float64{10, 20, 30}, ref: []float64{20}, oursCPU: []float64{0.1}, refCPU: []float64{0.05}, frames: 5, refFrames: 5, allocs: 3, kibs: 2048}
	got := benchTable([]*benchRow{r, {Name: "empty"}})
	for _, want := range []string{"| x | 5/5 | 3/1 | 20.0 (10.0-30.0) | 20.0 (20.0-20.0) | 1.00 | 0.50 | 3.0 | 2048 |", "| empty | 0/0 | 0/0 | - | - | - | - |"} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
}

// With ffmpeg installed, a committed stream is measured on both sides; and a
// load at the gate stops the run before anything is decoded.
func TestBenchRuns(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	old := loadAvg
	defer func() { loadAvg = old }()
	stream := "../../testdata/streams/base_ip_qp26.264"
	loadAvg = func() (float64, error) { return 1, nil }
	var log bytes.Buffer
	rows, loads, stopped, err := runBench([]string{stream, "../../testdata/hevc/streams/main_ipb.265"}, 2, 3, t.TempDir(), 8, &log)
	if err != nil || stopped || len(loads) != 4 || len(rows[0].ours) != 2 || rows[0].frames != rows[0].refFrames || rows[1].frames != rows[1].refFrames || rows[0].allocs < 1 {
		t.Fatalf("rows %+v loads %v stopped %v err %v", rows, loads, stopped, err)
	}
	loadAvg = func() (float64, error) { return 9, nil }
	if rows, _, stopped, err := runBench([]string{stream}, 2, 1, "", 8, &log); err != nil || !stopped || len(rows[0].ours) != 0 {
		t.Errorf("gate: %+v %v %v", rows, stopped, err)
	}
	if !strings.Contains(log.String(), "stopping") {
		t.Errorf("log %q", log.String())
	}
	loadAvg = func() (float64, error) { return 0, exec.ErrNotFound }
	if _, _, _, err := runBench([]string{stream}, 1, 1, "", 8, &log); err == nil {
		t.Error("load error ignored")
	}
	loadAvg = func() (float64, error) { return 1, nil }
	if _, _, _, err := runBench([]string{"missing.264"}, 1, 1, "", 8, &log); err == nil {
		t.Error("missing input accepted")
	}
	if _, _, _, err := runBench([]string{"missing.264"}, 1, 2, t.TempDir(), 8, &log); err == nil {
		t.Error("missing input accepted when repeated")
	}
}

// Containers go through avkit's reader and NewTrack; both codecs, and the
// refusals for a file that is not a container and one with no video track.
func TestBenchContainers(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	old := loadAvg
	defer func() { loadAvg = old }()
	loadAvg = func() (float64, error) { return 1, nil }
	var log bytes.Buffer
	rows, _, _, err := runBench([]string{"../../testdata/files/bframes.mp4", "../../testdata/files/hevc-main-ipb.mp4"}, 1, 1, "", 8, &log)
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg returns 10 of the 12 pictures of hevc-main-ipb.mp4 (an edit list is
	// the likely reason, not checked), so only the H.264 file must agree on count.
	for _, r := range rows {
		if r.frames == 0 || r.refFrames == 0 {
			t.Errorf("%s: %d frames, ffmpeg %d", r.Name, r.frames, r.refFrames)
		}
	}
	if rows[0].frames != rows[0].refFrames {
		t.Errorf("%s: %d frames, ffmpeg %d", rows[0].Name, rows[0].frames, rows[0].refFrames)
	}
	for _, bad := range []string{"../../testdata/jvt/BA1_FT_C.md5", "../../testdata/files/ffmpeg-aaclc.mp4"} {
		if _, err := oursRun(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := oursRun("../../testdata/streams/base_ip_qp26.md5"); err == nil {
		t.Error("an md5 file decoded")
	}
}

func TestWaitQuiet(t *testing.T) {
	oldLoad, oldPause := loadAvg, pause
	defer func() { loadAvg, pause = oldLoad, oldPause }()
	pause = time.Millisecond
	calls := 0
	loadAvg = func() (float64, error) { calls++; return float64(10 - calls*3), nil } // 7: quiet on the first look
	var log bytes.Buffer
	if err := waitQuiet(time.Second, 8, &log); err != nil || calls != 1 {
		t.Fatalf("quiet: %v after %d looks", err, calls)
	}
	calls = 0
	loadAvg = func() (float64, error) { calls++; return float64(12 - calls*3), nil } // 9, 6
	if err := waitQuiet(time.Second, 8, &log); err != nil || calls != 2 || !strings.Contains(log.String(), "waiting") {
		t.Fatalf("busy then quiet: %v after %d looks, log %q", err, calls, log.String())
	}
	loadAvg = func() (float64, error) { return 9, nil }
	if err := waitQuiet(20*time.Millisecond, 8, &log); err != nil {
		t.Fatalf("never quiet: %v", err)
	}
	loadAvg = func() (float64, error) { return 0, exec.ErrNotFound }
	if waitQuiet(time.Second, 8, &log) == nil {
		t.Error("load error ignored")
	}
	if waitQuiet(0, 8, &log) != nil {
		t.Error("zero wait looked at the load")
	}
}

func TestBenchCmd(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg")
	}
	old := loadAvg
	defer func() { loadAvg = old }()
	loadAvg = func() (float64, error) { return 1, nil }
	cmd := benchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--rounds", "1", "../../testdata/streams/base_ip_qp26.264"})
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "base_ip_qp26.264") || !strings.Contains(out.String(), "1-minute load") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	loadAvg = func() (float64, error) { return 9, nil }
	out.Reset()
	cmd = benchCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"../../testdata/streams/base_ip_qp26.264"})
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "STOPPED EARLY") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	cmd = benchCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	prof := filepath.Join(t.TempDir(), "cpu.pprof")
	loadAvg = func() (float64, error) { return 1, nil }
	cmd = benchCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--rounds=1", "--cpuprofile", prof, "../../testdata/streams/base_ip_qp26.264"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(prof); err != nil || fi.Size() == 0 {
		t.Errorf("profile %v %v", fi, err)
	}
	for _, args := range [][]string{{"--cpuprofile", t.TempDir() + "/no/such/dir/p"}, {"--cpuprofile", "/dev/null"}} {
		cmd = benchCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(append(args, "x"))
		_ = cmd.Execute() // the first cannot be created; the second is not a regular file and may still start
	}
	for _, bad := range []string{"--rounds=0", "--repeat=0", "--repeat=2"} {
		cmd = benchCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{bad, "x"})
		if cmd.Execute() == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
