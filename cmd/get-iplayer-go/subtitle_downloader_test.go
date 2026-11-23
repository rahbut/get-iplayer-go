package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeMP4Box(boxType string, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], uint32(len(buf)))
	copy(buf[4:8], boxType)
	copy(buf[8:], payload)
	return buf
}

func makeMP4LargeBox(boxType string, payload []byte) []byte {
	buf := make([]byte, 16+len(payload))
	binary.BigEndian.PutUint32(buf[0:4], 1)
	copy(buf[4:8], boxType)
	binary.BigEndian.PutUint64(buf[8:16], uint64(len(buf)))
	copy(buf[16:], payload)
	return buf
}

func makeTrack(handlerType string, tkhdFlags uint32) []byte {
	// tkhd FullBox: 1 byte version + 3 bytes flags + minimal body
	tkhdPayload := make([]byte, 16)
	tkhdPayload[1] = byte((tkhdFlags >> 16) & 0xff)
	tkhdPayload[2] = byte((tkhdFlags >> 8) & 0xff)
	tkhdPayload[3] = byte(tkhdFlags & 0xff)
	tkhd := makeMP4Box("tkhd", tkhdPayload)

	// hdlr FullBox: 4 bytes ver/flags + 4 bytes pre_defined + 4 bytes handler_type
	hdlrPayload := make([]byte, 16)
	copy(hdlrPayload[8:12], handlerType)
	hdlr := makeMP4Box("hdlr", hdlrPayload)
	mdia := makeMP4Box("mdia", hdlr)

	var trakPayload bytes.Buffer
	trakPayload.Write(tkhd)
	trakPayload.Write(mdia)
	return makeMP4Box("trak", trakPayload.Bytes())
}

func readTrackFlags(t *testing.T, mp4Path string) map[string]uint32 {
	t.Helper()
	f, err := os.Open(mp4Path)
	if err != nil {
		t.Fatalf("open mp4: %v", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("stat mp4: %v", err)
	}

	got := make(map[string]uint32)
	topBoxes, err := readMP4Boxes(f, 0, info.Size())
	if err != nil {
		t.Fatalf("read top boxes: %v", err)
	}
	for _, top := range topBoxes {
		if top.boxType != "moov" {
			continue
		}
		children, err := readMP4Boxes(f, top.dataStart, top.end)
		if err != nil {
			t.Fatalf("read moov children: %v", err)
		}
		for _, child := range children {
			if child.boxType != "trak" {
				continue
			}
			var flags uint32
			var handler string
			tchildren, err := readMP4Boxes(f, child.dataStart, child.end)
			if err != nil {
				t.Fatalf("read trak children: %v", err)
			}
			for _, tc := range tchildren {
				switch tc.boxType {
				case "tkhd":
					var vf [4]byte
					if _, err := f.ReadAt(vf[:], tc.dataStart); err != nil {
						t.Fatalf("read tkhd: %v", err)
					}
					flags = binary.BigEndian.Uint32(vf[:]) & 0x00ffffff
				case "mdia":
					mchildren, err := readMP4Boxes(f, tc.dataStart, tc.end)
					if err != nil {
						t.Fatalf("read mdia children: %v", err)
					}
					for _, mc := range mchildren {
						if mc.boxType == "hdlr" {
							var ht [4]byte
							if _, err := f.ReadAt(ht[:], mc.dataStart+8); err != nil {
								t.Fatalf("read hdlr: %v", err)
							}
							handler = string(ht[:])
						}
					}
				}
			}
			got[handler] = flags
		}
	}
	return got
}

func TestClearSubtitleDefaultFlag(t *testing.T) {
	var mp4 bytes.Buffer
	mp4.Write(makeMP4Box("ftyp", []byte("isom\x00\x00\x02\x00isom")))
	// Use a 64-bit largesize mdat before moov to exercise extended box sizes
	mp4.Write(makeMP4LargeBox("mdat", []byte("dummy media payload")))

	var moovPayload bytes.Buffer
	moovPayload.Write(makeTrack("vide", 0x000003))
	moovPayload.Write(makeTrack("soun", 0x000003))
	moovPayload.Write(makeTrack("sbtl", 0x000003))
	mp4.Write(makeMP4Box("moov", moovPayload.Bytes()))

	mp4Path := filepath.Join(t.TempDir(), "test.mp4")
	if err := os.WriteFile(mp4Path, mp4.Bytes(), 0644); err != nil {
		t.Fatalf("write test mp4: %v", err)
	}

	if err := clearSubtitleDefaultFlag(mp4Path); err != nil {
		t.Fatalf("clearSubtitleDefaultFlag failed: %v", err)
	}

	flags := readTrackFlags(t, mp4Path)
	if flags["vide"] != 0x000003 {
		t.Errorf("vide tkhd flags = 0x%06x, want 0x000003", flags["vide"])
	}
	if flags["soun"] != 0x000003 {
		t.Errorf("soun tkhd flags = 0x%06x, want 0x000003", flags["soun"])
	}
	if flags["sbtl"] != 0x000002 {
		t.Errorf("sbtl tkhd flags = 0x%06x, want 0x000002", flags["sbtl"])
	}

	// Idempotent on second call
	if err := clearSubtitleDefaultFlag(mp4Path); err != nil {
		t.Fatalf("second clearSubtitleDefaultFlag failed: %v", err)
	}
	flags = readTrackFlags(t, mp4Path)
	if flags["sbtl"] != 0x000002 {
		t.Errorf("after second call sbtl tkhd flags = 0x%06x, want 0x000002", flags["sbtl"])
	}
}

func TestMuxStreamsSubtitleNotDefault(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not available")
	}

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "video.mp4")
	audioPath := filepath.Join(dir, "audio.m4a")
	srtPath := filepath.Join(dir, "subs.srt")
	outPath := filepath.Join(dir, "out.mp4")

	// Generate 1s test video and audio
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "color=c=black:s=160x90:d=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", videoPath).CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v (%s)", err, string(out))
	}
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=stereo", "-t", "1",
		"-c:a", "aac", audioPath).CombinedOutput(); err != nil {
		t.Fatalf("generate audio: %v (%s)", err, string(out))
	}
	srtContent := "1\n00:00:00,000 --> 00:00:00,900\nHello world\n"
	if err := os.WriteFile(srtPath, []byte(srtContent), 0644); err != nil {
		t.Fatalf("write srt: %v", err)
	}

	if err := muxStreams(videoPath, audioPath, srtPath, outPath); err != nil {
		t.Fatalf("muxStreams failed: %v", err)
	}

	assertStreamDispositions := func(stage string) {
		t.Helper()
		probeOut, err := exec.Command("ffprobe", "-v", "error",
			"-show_entries", "stream=codec_type:stream_disposition=default,forced:stream_tags=language",
			"-of", "compact=p=0", outPath).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: ffprobe failed: %v (%s)", stage, err, string(probeOut))
		}
		lines := strings.Split(strings.TrimSpace(string(probeOut)), "\n")
		var foundSub bool
		for _, line := range lines {
			if strings.Contains(line, "codec_type=subtitle") {
				foundSub = true
				if !strings.Contains(line, "disposition:default=0") {
					t.Errorf("%s: subtitle stream still default: %s", stage, line)
				}
				if !strings.Contains(line, "disposition:forced=0") {
					t.Errorf("%s: subtitle stream marked forced: %s", stage, line)
				}
				if !strings.Contains(line, "tag:language=eng") {
					t.Errorf("%s: subtitle stream missing language=eng: %s", stage, line)
				}
			} else if strings.Contains(line, "codec_type=video") || strings.Contains(line, "codec_type=audio") {
				if !strings.Contains(line, "disposition:default=1") {
					t.Errorf("%s: video/audio stream lost default flag: %s", stage, line)
				}
			}
		}
		if !foundSub {
			t.Fatalf("%s: no subtitle stream found in ffprobe output:\n%s", stage, string(probeOut))
		}
	}

	assertStreamDispositions("after muxStreams")

	// Also verify TagMP4File preserves default=0 on the subtitle stream
	meta := &ProgrammeMetadata{
		PID:          "test1234",
		ShowName:     "Test Show",
		EpisodeTitle: "Test Episode",
		SeriesNum:    1,
		EpisodeNum:   1,
		Channel:      "BBC One",
	}
	if err := TagMP4File(outPath, meta, "1920x1080"); err != nil {
		t.Fatalf("TagMP4File failed: %v", err)
	}
	assertStreamDispositions("after TagMP4File")

	// Verify the subtitle payload is still intact and decodable
	extracted, err := exec.Command("ffmpeg", "-v", "error", "-i", outPath, "-map", "0:s:0", "-f", "srt", "pipe:1").CombinedOutput()
	if err != nil {
		t.Fatalf("extract subtitle failed: %v (%s)", err, string(extracted))
	}
	if !strings.Contains(string(extracted), "Hello world") {
		t.Errorf("extracted subtitle missing cue text: %q", string(extracted))
	}
}
