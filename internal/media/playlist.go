package media

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Bandwidth is measured from the real chunks ffmpeg wrote, which is exactly
// what the HLS spec asks for: BANDWIDTH is the peak bitrate of any chunk,
// AVERAGE-BANDWIDTH the average over the whole rendition.
type Bandwidth struct {
	Peak         int // bits per second
	Average      int
	Segments     int
	FirstSegment string // file name, used to probe the output size
}

// MeasureBandwidth reads dir/index.m3u8 and the sizes of its chunks.
func MeasureBandwidth(dir string) (Bandwidth, error) {
	f, err := os.Open(filepath.Join(dir, "index.m3u8"))
	if err != nil {
		return Bandwidth{}, err
	}
	defer f.Close()

	var bw Bandwidth
	var totalBits, totalSec float64
	var pendingDur float64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimPrefix(line, "#EXTINF:")
			v, _, _ = strings.Cut(v, ",")
			pendingDur, _ = strconv.ParseFloat(v, 64)
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			st, err := os.Stat(filepath.Join(dir, line))
			if err != nil {
				return Bandwidth{}, fmt.Errorf("chunk %s: %w", line, err)
			}
			if bw.FirstSegment == "" {
				bw.FirstSegment = line
			}
			bits := float64(st.Size() * 8)
			if pendingDur > 0 {
				if rate := int(bits / pendingDur); rate > bw.Peak {
					bw.Peak = rate
				}
			}
			totalBits += bits
			totalSec += pendingDur
			bw.Segments++
			pendingDur = 0
		}
	}
	if err := sc.Err(); err != nil {
		return Bandwidth{}, err
	}
	if bw.Segments == 0 || totalSec <= 0 {
		return Bandwidth{}, fmt.Errorf("no chunks listed in %s", filepath.Join(dir, "index.m3u8"))
	}
	bw.Average = int(totalBits / totalSec)
	return bw, nil
}

// Variant is one line pair in master.m3u8.
type Variant struct {
	URI          string
	Bandwidth    int
	AvgBandwidth int
	Width        int
	Height       int
	FPS          float64
	Codecs       string
}

// MasterPlaylist renders master.m3u8: the "menu" the player reads first.
// Variants are listed highest bandwidth first.
func MasterPlaylist(vs []Variant) []byte {
	sorted := append([]Variant(nil), vs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Bandwidth > sorted[j].Bandwidth })
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, v := range sorted {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,RESOLUTION=%dx%d,FRAME-RATE=%.3f,CODECS=\"%s\"\n%s\n",
			v.Bandwidth, v.AvgBandwidth, v.Width, v.Height, v.FPS, v.Codecs, v.URI)
	}
	return []byte(b.String())
}
