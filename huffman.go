package decalgo

import (
	"container/heap"
	"errors"
	"math"
)

type huffmanNode struct {
	weight      float64
	order       int
	candidate   int
	left, right *huffmanNode
}

type huffmanHeap []*huffmanNode

func (h huffmanHeap) Len() int { return len(h) }
func (h huffmanHeap) Less(i, j int) bool {
	if h[i].weight == h[j].weight {
		return h[i].order < h[j].order
	}
	return h[i].weight < h[j].weight
}
func (h huffmanHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *huffmanHeap) Push(x any)   { *h = append(*h, x.(*huffmanNode)) }
func (h *huffmanHeap) Pop() any     { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }

func buildHuffman(candidates []TokenCandidate, temperature float64) (*huffmanNode, error) {
	if len(candidates) < 2 {
		return nil, errors.New("Huffman coding requires at least two candidates")
	}
	maxScore := candidates[0].LogProb
	for _, c := range candidates[1:] {
		if c.LogProb > maxScore {
			maxScore = c.LogProb
		}
	}
	h := make(huffmanHeap, 0, len(candidates))
	for i, c := range candidates {
		weight := math.Exp((c.LogProb - maxScore) / temperature)
		if math.IsNaN(weight) || math.IsInf(weight, 0) {
			return nil, errors.New("invalid model score")
		}
		h = append(h, &huffmanNode{weight: weight, order: i, candidate: i})
	}
	heap.Init(&h)
	nextOrder := len(candidates)
	for h.Len() > 1 {
		a := heap.Pop(&h).(*huffmanNode)
		b := heap.Pop(&h).(*huffmanNode)
		heap.Push(&h, &huffmanNode{weight: a.weight + b.weight, order: nextOrder, candidate: -1, left: a, right: b})
		nextOrder++
	}
	return heap.Pop(&h).(*huffmanNode), nil
}

func (n *huffmanNode) codeFor(candidate int, prefix []int) ([]int, bool) {
	if n.candidate >= 0 {
		if n.candidate == candidate {
			return append([]int(nil), prefix...), true
		}
		return nil, false
	}
	if code, ok := n.left.codeFor(candidate, append(prefix, 0)); ok {
		return code, true
	}
	return n.right.codeFor(candidate, append(prefix, 1))
}
