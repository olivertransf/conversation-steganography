package decalgo

import (
	"errors"
	"math"
	"sort"
)

const arithmeticTotal uint64 = 32768
const arithmeticHalf uint64 = 0x80000000
const arithmeticQuarter uint64 = 0x40000000
const arithmeticThreeQuarter uint64 = 0xC0000000

type frequencyTable struct{ cumulative []uint64 }

func makeFrequencies(candidates []TokenCandidate, temperature float64) (frequencyTable, error) {
	if len(candidates) < 2 || uint64(len(candidates)) >= arithmeticTotal {
		return frequencyTable{}, errors.New("invalid arithmetic candidate count")
	}
	maxScore := candidates[0].LogProb
	for _, c := range candidates[1:] {
		if c.LogProb > maxScore {
			maxScore = c.LogProb
		}
	}
	weights := make([]float64, len(candidates))
	sum := 0.0
	for i, c := range candidates {
		weights[i] = math.Exp((c.LogProb - maxScore) / temperature)
		if math.IsNaN(weights[i]) || math.IsInf(weights[i], 0) {
			return frequencyTable{}, errors.New("invalid model score")
		}
		sum += weights[i]
	}
	available := arithmeticTotal - uint64(len(candidates))
	counts := make([]uint64, len(candidates))
	fraction := make([]float64, len(candidates))
	used := uint64(0)
	for i, weight := range weights {
		exact := weight / sum * float64(available)
		base := uint64(math.Floor(exact))
		counts[i] = base + 1
		fraction[i] = exact - float64(base)
		used += counts[i]
	}
	order := make([]int, len(candidates))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		if fraction[order[i]] == fraction[order[j]] {
			return order[i] < order[j]
		}
		return fraction[order[i]] > fraction[order[j]]
	})
	for i := uint64(0); used < arithmeticTotal; i++ {
		counts[order[i%uint64(len(order))]]++
		used++
	}
	cumulative := make([]uint64, len(counts)+1)
	for i, count := range counts {
		cumulative[i+1] = cumulative[i] + count
	}
	return frequencyTable{cumulative: cumulative}, nil
}

type arithmeticDecoder struct {
	low, high, code uint64
	read            func() int
}

func newArithmeticDecoder(read func() int) *arithmeticDecoder {
	d := &arithmeticDecoder{high: 0xffffffff, read: read}
	for i := 0; i < 32; i++ {
		d.code = (d.code << 1) | uint64(read())
	}
	return d
}

func (d *arithmeticDecoder) symbol(f frequencyTable) int {
	rangeSize := d.high - d.low + 1
	value := ((d.code-d.low+1)*arithmeticTotal - 1) / rangeSize
	symbol := sort.Search(len(f.cumulative)-1, func(i int) bool { return f.cumulative[i+1] > value })
	d.high = d.low + rangeSize*f.cumulative[symbol+1]/arithmeticTotal - 1
	d.low = d.low + rangeSize*f.cumulative[symbol]/arithmeticTotal
	for {
		if d.high < arithmeticHalf {
		} else if d.low >= arithmeticHalf {
			d.low -= arithmeticHalf
			d.high -= arithmeticHalf
			d.code -= arithmeticHalf
		} else if d.low >= arithmeticQuarter && d.high < arithmeticThreeQuarter {
			d.low -= arithmeticQuarter
			d.high -= arithmeticQuarter
			d.code -= arithmeticQuarter
		} else {
			break
		}
		d.low = (d.low << 1) & 0xffffffff
		d.high = ((d.high << 1) | 1) & 0xffffffff
		d.code = ((d.code << 1) | uint64(d.read())) & 0xffffffff
	}
	return symbol
}

type arithmeticEncoder struct {
	low, high uint64
	pending   int
	emit      func(int)
}

func newArithmeticEncoder(emit func(int)) *arithmeticEncoder {
	return &arithmeticEncoder{high: 0xffffffff, emit: emit}
}
func (e *arithmeticEncoder) symbol(symbol int, f frequencyTable) {
	rangeSize := e.high - e.low + 1
	e.high = e.low + rangeSize*f.cumulative[symbol+1]/arithmeticTotal - 1
	e.low = e.low + rangeSize*f.cumulative[symbol]/arithmeticTotal
	for {
		if e.high < arithmeticHalf {
			e.output(0)
		} else if e.low >= arithmeticHalf {
			e.output(1)
			e.low -= arithmeticHalf
			e.high -= arithmeticHalf
		} else if e.low >= arithmeticQuarter && e.high < arithmeticThreeQuarter {
			e.pending++
			e.low -= arithmeticQuarter
			e.high -= arithmeticQuarter
		} else {
			break
		}
		e.low = (e.low << 1) & 0xffffffff
		e.high = ((e.high << 1) | 1) & 0xffffffff
	}
}
func (e *arithmeticEncoder) output(bit int) {
	e.emit(bit)
	for e.pending > 0 {
		e.emit(bit ^ 1)
		e.pending--
	}
}
