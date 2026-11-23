package media

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseProbeRotatedPhoneClip(t *testing.T) {
	// An iPhone portrait video: stored 1920x1080 with a -90° display matrix.
	js := `{"streams":[
	  {"codec_type":"video","codec_name":"hevc","width":1920,"height":1080,
	   "avg_frame_rate":"30000/1001","r_frame_rate":"30/1",
	   "side_data_list":[{"rotation":-90}]},
	  {"codec_type":"audio","codec_name":"aac"}],
	 "format":{"duration":"12.480000"}}`
	p, err := parseProbe([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 1080 || p.Height != 1920 {
		t.Errorf("display size = %dx%d, want 1080x1920", p.Width, p.Height)
	}
	if p.FPS != 29.97 || !p.HasAudio || p.Duration != 12.48 || p.VideoCodec != "hevc" {
		t.Errorf("unexpected probe result %+v", p)
	}
}

func TestParseProbeNoVideo(t *testing.T) {
	_, err := parseProbe([]byte(`{"streams":[{"codec_type":"audio"}],"format":{"duration":"3"}}`))
	if err == nil {
		t.Fatal("want error for audio-only file")
	}
}

func names(rs []Rendition) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func TestPlanLadder(t *testing.T) {
	cases := []struct {
		name string
		src  ProbeResult
		want []string
	}{
		{"1080p landscape", ProbeResult{Width: 1920, Height: 1080, FPS: 30}, []string{"1080p", "720p", "480p"}},
		{"720p source never upscales", ProbeResult{Width: 1280, Height: 720, FPS: 30}, []string{"720p", "480p"}},
		{"portrait reel uses short side", ProbeResult{Width: 1080, Height: 1920, FPS: 30}, []string{"1080p", "720p", "480p"}},
		{"tiny source keeps its size", ProbeResult{Width: 640, Height: 361, FPS: 25}, []string{"360p"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := names(Plan(c.src, 2)); !slices.Equal(got, c.want) {
				t.Errorf("Plan = %v, want %v", got, c.want)
			}
		})
	}
}

func TestPlanGOPAndLevels(t *testing.T) {
	p := Plan(ProbeResult{Width: 1920, Height: 1080, FPS: 59.94}, 2)
	if p[0].GOP != 120 {
		t.Errorf("GOP = %d, want 120 (2 s at ~60 fps)", p[0].GOP)
	}
	if p[0].Level != "4.2" || p[1].Level != "3.2" {
		t.Errorf("levels = %s, %s; want 4.2, 3.2 for 60 fps", p[0].Level, p[1].Level)
	}
	if got := p[0].Codecs(true); got != "avc1.4d402a,mp4a.40.2" {
		t.Errorf("codecs = %s", got)
	}
	high := Plan(ProbeResult{Width: 1280, Height: 720, FPS: 120}, 2)
	if !high[0].CapFPS || high[0].GOP != 120 || !strings.Contains(high[0].VideoFilter(), "fps=60") {
		t.Errorf("120 fps source should be capped at 60: %+v", high[0])
	}
}

func TestVideoFilterOrientation(t *testing.T) {
	land := Plan(ProbeResult{Width: 1920, Height: 1080, FPS: 30}, 2)[1]
	if got := land.VideoFilter(); got != "scale=-2:720,format=yuv420p" {
		t.Errorf("landscape filter = %s", got)
	}
	port := Plan(ProbeResult{Width: 1080, Height: 1920, FPS: 30}, 2)[1]
	if got := port.VideoFilter(); got != "scale=720:-2,format=yuv420p" {
		t.Errorf("portrait filter = %s", got)
	}
}

func TestTranscodeArgsAlignKeyframes(t *testing.T) {
	r := Plan(ProbeResult{Width: 1920, Height: 1080, FPS: 30}, 2)[0]
	args := strings.Join(TranscodeArgs(TranscodeOptions{
		FFmpeg: "ffmpeg", Input: "in.mp4", OutDir: "out/1080p", Rendition: r,
		HasAudio: true, Duration: 10, Preset: "veryfast", SegmentSeconds: 6, KeyframeSeconds: 2,
	}), " ")
	for _, want := range []string{
		"-g 60 -keyint_min 60 -sc_threshold 0",
		"-force_key_frames expr:gte(t,n_forced*2)",
		"-hls_time 6", "-hls_playlist_type vod",
		"-map 0:a:0", "-c:a aac -b:a 128k",
		"-b:v 5000k -maxrate 5350k -bufsize 7500k",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args missing %q\n%s", want, args)
		}
	}
	silent := strings.Join(TranscodeArgs(TranscodeOptions{Rendition: r, Preset: "veryfast", SegmentSeconds: 6, KeyframeSeconds: 2}), " ")
	if strings.Contains(silent, "0:a:0") || strings.Contains(silent, "aac") {
		t.Errorf("no-audio source should not map audio: %s", silent)
	}
}

func TestParseProgress(t *testing.T) {
	in := "frame=1\nout_time_us=2500000\nprogress=continue\nout_time_ms=2600000\nout_time_us=N/A\nout_time_us=5000000\nprogress=end\n"
	var got []int
	ParseProgress(strings.NewReader(in), 10, func(p int) { got = append(got, p) })
	if want := []int{25, 26, 50, 100}; !slices.Equal(got, want) {
		t.Errorf("progress = %v, want %v", got, want)
	}
}

func TestMeasureBandwidthAndMaster(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("seg_0000.ts", 750_000) // 6,000,000 bits over 6 s = 1,000,000 b/s
	write("seg_0001.ts", 300_000) // 2,400,000 bits over 2 s = 1,200,000 b/s (peak)
	m3u8 := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000000,\nseg_0000.ts\n#EXTINF:2.000000,\nseg_0001.ts\n#EXT-X-ENDLIST\n"
	if err := os.WriteFile(filepath.Join(dir, "index.m3u8"), []byte(m3u8), 0o644); err != nil {
		t.Fatal(err)
	}
	bw, err := MeasureBandwidth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bw.Peak != 1_200_000 || bw.Average != 1_050_000 || bw.Segments != 2 || bw.FirstSegment != "seg_0000.ts" {
		t.Errorf("bandwidth = %+v", bw)
	}

	master := string(MasterPlaylist([]Variant{
		{URI: "480p/index.m3u8", Bandwidth: 1_200_000, AvgBandwidth: 1_050_000, Width: 854, Height: 480, FPS: 30, Codecs: "avc1.4d401e,mp4a.40.2"},
		{URI: "720p/index.m3u8", Bandwidth: 3_100_000, AvgBandwidth: 2_900_000, Width: 1280, Height: 720, FPS: 30, Codecs: "avc1.4d401f,mp4a.40.2"},
	}))
	want := "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=3100000,AVERAGE-BANDWIDTH=2900000,RESOLUTION=1280x720,FRAME-RATE=30.000,CODECS=\"avc1.4d401f,mp4a.40.2\"\n720p/index.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1200000,AVERAGE-BANDWIDTH=1050000,RESOLUTION=854x480,FRAME-RATE=30.000,CODECS=\"avc1.4d401e,mp4a.40.2\"\n480p/index.m3u8\n"
	if master != want {
		t.Errorf("master playlist:\n%s\nwant:\n%s", master, want)
	}
}
