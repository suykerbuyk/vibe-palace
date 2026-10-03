package heap

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

type Int int

func (i Int) Less(j Int) bool {
	return i < j
}

func TestHeap(t *testing.T) {
	h := Heap[Int]{}

	for i := 0; i < 20; i++ {
		h.Push(Int(rand.Int() % 100))
	}

	require.Equal(t, 20, h.Len())

	var inOrder []Int
	for h.Len() > 0 {
		inOrder = append(inOrder, h.Pop())
	}

	if !slices.IsSorted(inOrder) {
		t.Errorf("Heap did not return sorted elements: %+v", inOrder)
	}
}

func TestHeap_MaxAndPopLast(t *testing.T) {
	h := Heap[Int]{}
	values := []Int{5, 1, 9, 3, 7, 2, 8, 4, 6}
	for _, v := range values {
		h.Push(v)
	}

	require.Equal(t, Int(9), h.Max(), "Max should return the largest element")
	require.Equal(t, Int(1), h.Min(), "Min should return the smallest element")

	// PopLast should remove and return the maximum.
	popped := h.PopLast()
	require.Equal(t, Int(9), popped)
	require.Equal(t, Int(8), h.Max(), "Max should be 8 after removing 9")
	require.Equal(t, 8, h.Len())
}
