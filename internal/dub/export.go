package dub

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Export formats.
const (
	ExportMP4      = "mp4"      // the video with the original and dubbed tracks (engine: media.mux)
	ExportWAV      = "wav"      // one dubbed track
	ExportM4A      = "m4a"      // one dubbed track, AAC (engine: media.encode)
	ExportMP3      = "mp3"      // needs ffmpeg (not yet)
	ExportSRT      = "srt"      // subtitles (dual: translated + original)
	ExportVTT      = "vtt"      //
	ExportASS      = "ass"      // karaoke subtitles (word highlight)
	ExportStems    = "stems"    // ZIP: dubbed speech, background, original voices
	ExportSegments = "segments" // ZIP: every rendered line
	ExportBurn     = "burn"     // MP4 with burnt-in subtitles: needs ffmpeg (not yet)
)

// ExportFormats in the order the export panel lists them.
var ExportFormats = []string{ExportMP4, ExportWAV, ExportM4A, ExportMP3, ExportSRT, ExportVTT, ExportASS, ExportStems, ExportSegments, ExportBurn}

// ExportExt is the file extension of a format.
func ExportExt(format string) string {
	switch format {
	case ExportStems, ExportSegments:
		return "zip"
	case ExportBurn:
		return "mp4"
	}
	return format
}

// ExportName is a file name for an export: "<title>_<lang>.<ext>".
func ExportName(title, lang, format string) string {
	base := SafeName(strings.TrimSuffix(title, filepath.Ext(title)))
	if base == "" {
		base = "dub"
	}
	if lang != "" {
		base += "_" + lang
	}
	if format == ExportStems {
		base += "_stems"
	} else if format == ExportSegments {
		base += "_lines"
	}
	return base + "." + ExportExt(format)
}

var unsafeName = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]+`)

// SafeName makes s usable as a file name.
func SafeName(s string) string {
	s = strings.TrimSpace(unsafeName.ReplaceAllString(s, "_"))
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}

// ZipEntry is a file to put in a ZIP.
type ZipEntry struct {
	Name string // in the archive
	Path string // on disk
	Data []byte // or these bytes
}

// WriteZip writes entries into w (stored, not compressed: WAV and
// compressed audio do not shrink).
func WriteZip(w io.Writer, entries []ZipEntry) error {
	zw := zip.NewWriter(w)
	for _, e := range entries {
		hw, err := zw.CreateHeader(&zip.FileHeader{Name: e.Name, Method: zip.Store})
		if err != nil {
			return err
		}
		if e.Path == "" {
			if _, err := hw.Write(e.Data); err != nil {
				return err
			}
			continue
		}
		f, err := os.Open(e.Path)
		if err != nil {
			return err
		}
		_, err = io.Copy(hw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return zw.Close()
}

// SegmentEntries are the ZIP entries of lang's rendered lines:
// "003_Ana_00m12.4s.wav", in time order, from the files in dir.
func (p *Project) SegmentEntries(lang, dir string) []ZipEntry {
	t := p.Tracks[lang]
	var out []ZipEntry
	for i, s := range p.Segments {
		r, ok := t.Lines[s.ID]
		if !ok || r.File == "" {
			continue
		}
		m := int(s.Start) / 60
		sec := s.Start - float64(m*60)
		name := fmt.Sprintf("%03d_%s_%02dm%04.1fs.wav", i+1, SafeName(p.SpeakerName(s.Speaker)), m, sec)
		out = append(out, ZipEntry{Name: name, Path: filepath.Join(dir, r.File)})
	}
	return out
}
