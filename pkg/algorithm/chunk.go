package algorithm

import "github.com/samber/lo"

// ChunkSlice splits up the input slice into chunks
func ChunkSlice[T any](inputSlice []T, chunkSize int) [][]T {
	return lo.Chunk(inputSlice, chunkSize)
}
