package knowledge

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndzuki/obsidian-task-runner/pkg/yamlfrontmatter"
)

// searchCacheFile is the persisted BM25 index cache next to References/.
// Hidden from Obsidian's markdown views.
const searchCacheFile = ".kb-bm25.gob"

// searchIndexCache is the gob-serializable form of searchIndex: unexported
// fields cannot be encoded by gob, and termFreq is recomputed on decode.
type searchIndexCache struct {
	Fingerprint string
	Docs        []searchDocCache
	Postings    map[string][]int
	DocLen      []int
	AvgDocLen   float64
}

type searchDocCache struct {
	Path     string
	Title    string
	Summary  string
	Topics   string
	Aliases  string
	Tags     string
	Body     string
	Hits     int
}

func cacheFromIndex(idx *searchIndex, fp string) *searchIndexCache {
	c := &searchIndexCache{
		Fingerprint: fp,
		Postings:    idx.postings,
		DocLen:      idx.docLen,
		AvgDocLen:   idx.avgDocLen,
		Docs:        make([]searchDocCache, len(idx.docs)),
	}
	for i, d := range idx.docs {
		c.Docs[i] = searchDocCache{
			Path: d.Path, Title: d.Title, Summary: d.Summary,
			Topics: d.Topics, Aliases: d.Aliases, Tags: d.Tags,
			Body: d.Body, Hits: d.Hits,
		}
	}
	return c
}

func indexFromCache(c *searchIndexCache) *searchIndex {
	idx := &searchIndex{
		postings:   c.Postings,
		docLen:     c.DocLen,
		avgDocLen:  c.AvgDocLen,
		totalDocs:  len(c.Docs),
		docs:       make([]searchDoc, len(c.Docs)),
	}
	for i, d := range c.Docs {
		sd := searchDoc{
			Path: d.Path, Title: d.Title, Summary: d.Summary,
			Topics: d.Topics, Aliases: d.Aliases, Tags: d.Tags,
			Body: d.Body, Hits: d.Hits,
		}
		text := strings.ToLower(sd.Title + " " + sd.Summary + " " + sd.Topics + " " + sd.Aliases + " " + sd.Tags + " " + sd.Body)
		sd.termFreq = make(map[string]int, 32)
		for _, t := range tokenize(text) {
			sd.termFreq[t]++
		}
		idx.docs[i] = sd
	}
	return idx
}

// corpusFingerprint hashes every scanned .md's path + mtime + size so any
// corpus change invalidates the cache exactly once. skipArchived is part of
// the fingerprint implicitly: archived files are simply not scanned.
func corpusFingerprint(refsDir string, skipArchived bool) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(refsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") || d.Name() == "INDEX.md" {
			return nil
		}
		if skipArchived && strings.HasPrefix(filepath.ToSlash(path), filepath.ToSlash(refsDir)+"/archived/") {
			return nil
		}
		info, serr := d.Info()
		if serr != nil {
			return nil
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(path), info.Size(), info.ModTime().UnixNano())
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// BuildSearchIndexCached returns the BM25 index, reusing the persisted gob
// cache when the corpus fingerprint is unchanged. A cache miss rebuilds the
// index and atomically persists it. Cache I/O failures are non-fatal: the
// in-memory index is still returned.
func BuildSearchIndexCached(vaultDir string, skipArchived bool) (*searchIndex, error) {
	refsDir := filepath.Join(vaultDir, "References")
	fp, err := corpusFingerprint(refsDir, skipArchived)
	if err != nil {
		return nil, err
	}
	cachePath := filepath.Join(refsDir, searchCacheFile)
	if data, rerr := os.ReadFile(cachePath); rerr == nil {
		var cached searchIndexCache
		if gob.NewDecoder(bytes.NewReader(data)).Decode(&cached) == nil &&
			cached.Fingerprint == fp && len(cached.Docs) > 0 {
			return indexFromCache(&cached), nil
		}
	}
	idx, err := buildSearchIndexFiltered(refsDir, skipArchived)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(cacheFromIndex(idx, fp)); err != nil {
		return idx, nil // non-fatal: cache write failed
	}
	_ = yamlfrontmatter.AtomicWrite(cachePath, buf.Bytes())
	return idx, nil
}
