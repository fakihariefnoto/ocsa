package storage

import (
	"syscall"
)

// Composition is what the database file is actually made of. The single
// live/free split was not informative: a file can be 99% full and still be
// mostly dead weight, because the frozen generation is real rows that no
// vacuum can touch. Four categories, and they always sum to the file size.
type Composition struct {
	Total  int64 // the file on disk
	Live   int64 // the generation OpenCode still writes
	Frozen int64 // the generation it abandoned
	Other  int64 // indexes, key/value state, schema
	Free   int64 // freelist pages, returnable by a vacuum

	DiskFree  int64 // filesystem free space, for scale
	DiskTotal int64
}

// Segments returns the four parts in display order.
func (c Composition) Segments() []Segment {
	return []Segment{
		{Key: "live", Label: "live", Bytes: c.Live},
		{Key: "frozen", Label: "frozen", Bytes: c.Frozen},
		{Key: "other", Label: "other", Bytes: c.Other},
		{Key: "reclaimable", Label: "reclaimable", Bytes: c.Free},
	}
}

// Segment is one part of the composition.
type Segment struct {
	Key   string
	Label string
	Bytes int64
}

// Percent of the file this segment occupies, rounded to a tenth.
func (s Segment) Percent(total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(s.Bytes) / float64(total) * 100
}

// Ratio is how many times bigger the file is than the data in active use. A
// number a reader can act on: 1.0x means nothing is wasted.
func (c Composition) Ratio() float64 {
	if c.Live <= 0 {
		return 0
	}
	return float64(c.Total) / float64(c.Live)
}

// DiskShare is the file as a percentage of the free space on its volume, which
// is the context that says whether any of this matters.
func (c Composition) DiskShare() float64 {
	if c.DiskFree <= 0 {
		return 0
	}
	return float64(c.Total) / float64(c.DiskFree) * 100
}

// Composition derives the composition from a finished scan.
func (s *Scan) Composition() Composition {
	c := Composition{
		Total:     s.Stats.FileBytes(),
		Free:      s.Stats.FreeBytes(),
		DiskFree:  s.DiskFree,
		DiskTotal: s.DiskTotal,
	}
	for _, g := range s.Generations {
		if g.Frozen {
			c.Frozen += g.Bytes
		} else {
			c.Live += g.Bytes
		}
	}
	c.Other = c.Total - c.Live - c.Frozen - c.Free
	if c.Other < 0 {
		c.Other = 0
	}
	return c
}

// diskSpace reports free and total bytes on the filesystem holding path.
// Failure is not an error: the composition is still useful without it.
func diskSpace(path string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bsize := int64(st.Bsize)
	return int64(st.Bavail) * bsize, int64(st.Blocks) * bsize
}
