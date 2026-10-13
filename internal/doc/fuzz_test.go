package doc

import (
	"bytes"
	"context"
	"testing"
)

func FuzzReaders(f *testing.F) {
	f.Add([]byte("Chapter 1\nHi"))
	f.Add([]byte("# A\n**b** [c](d)"))
	f.Add(makeZipF([]zipFile{{"META-INF/container.xml", container}, {"OEBPS/content.opf", "<package/>"}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, fm := range []Format{TXT, Markdown, EPUB, DOCX, SRT} {
			Read(context.Background(), fm, bytes.NewReader(b), int64(len(b)), Limits{MaxUncompressed: 1 << 20})
		}
	})
}
