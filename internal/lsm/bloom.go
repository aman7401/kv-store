package lsm

import (
	"hash/fnv"
	"math"
)

// BloomFilter is a probabilistic data structure for set membership testing.
// False positives are possible; false negatives are not.
// Used to skip SSTable disk reads when a key definitely isn't present.
type BloomFilter struct {
	bits []bool
	k    int // number of hash functions
	m    int // number of bits
}

// NewBloomFilter creates a filter sized for n expected items at false-positive rate p.
func NewBloomFilter(n int, p float64) *BloomFilter {
	m := optimalM(n, p)
	k := optimalK(m, n)
	return &BloomFilter{bits: make([]bool, m), k: k, m: m}
}

func optimalM(n int, p float64) int {
	return int(math.Ceil(-float64(n) * math.Log(p) / (math.Log(2) * math.Log(2))))
}

func optimalK(m, n int) int {
	return int(math.Ceil(float64(m) / float64(n) * math.Log(2)))
}

func (f *BloomFilter) Add(key string) {
	for i := 0; i < f.k; i++ {
		f.bits[f.hash(key, i)] = true
	}
}

// Contains returns false if the key is definitely absent, true if it might be present.
func (f *BloomFilter) Contains(key string) bool {
	for i := 0; i < f.k; i++ {
		if !f.bits[f.hash(key, i)] {
			return false
		}
	}
	return true
}

func (f *BloomFilter) hash(key string, seed int) int {
	h := fnv.New64a()
	h.Write([]byte{byte(seed >> 8), byte(seed)})
	h.Write([]byte(key))
	return int(h.Sum64() % uint64(f.m))
}
