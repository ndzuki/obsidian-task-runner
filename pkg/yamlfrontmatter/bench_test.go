package yamlfrontmatter

import (
	"path/filepath"
	"testing"
)

// BenchmarkNormalizeWrite compares the fsync vs no-sync write paths used by
// scan-time normalization. With thousands of legacy documents awaiting a
// one-time backfill, the fsync cost dominates the pass; the no-sync path is
// the deployed one (atomicWriteNoSync).
func BenchmarkNormalizeWrite(b *testing.B) {
	for _, sync := range []bool{true, false} {
		b.Run(map[bool]string{true: "fsync", false: "no-sync"}[sync], func(b *testing.B) {
			dir := b.TempDir()
			// One file per iteration keeps the working set small and the
			// measurement focused on the write path.
			path := filepath.Join(dir, "doc.md")
			data := []byte("---\nid: \"001\"\ntitle: Doc\ncreated: \"2026-08-12T00:00:00+08:00\"\nupdated: \"2026-08-12T00:00:00+08:00\"\n---\n# Body\n")
			writer := atomicWrite
			if !sync {
				writer = atomicWriteNoSync
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := writer(path, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
