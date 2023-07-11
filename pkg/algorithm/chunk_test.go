package algorithm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_ChunkSliceString(t *testing.T) {
	type args struct {
		targets   []string
		chunkSize int
	}
	tests := []struct {
		name string
		args args
		want [][]string
	}{
		{
			name: "can be evenly chunked",
			args: args{
				targets:   []string{"a", "b", "c", "d"},
				chunkSize: 2,
			},
			want: [][]string{
				{"a", "b"},
				{"c", "d"},
			},
		},
		{
			name: "cannot be evenly chunked",
			args: args{
				targets:   []string{"a", "b", "c", "d"},
				chunkSize: 3,
			},
			want: [][]string{
				{"a", "b", "c"},
				{"d"},
			},
		},
		{
			name: "chunkSize equal to total count",
			args: args{
				targets:   []string{"a", "b", "c", "d"},
				chunkSize: 4,
			},
			want: [][]string{
				{"a", "b", "c", "d"},
			},
		},
		{
			name: "chunkSize greater than total count",
			args: args{
				targets:   []string{"a", "b", "c", "d"},
				chunkSize: 5,
			},
			want: [][]string{
				{"a", "b", "c", "d"},
			},
		},
		{
			name: "chunk nil slice",
			args: args{
				targets:   nil,
				chunkSize: 2,
			},
			want: [][]string{},
		},
		{
			name: "chunk empty slice",
			args: args{
				targets:   []string{},
				chunkSize: 2,
			},
			want: [][]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkSlice(tt.args.targets, tt.args.chunkSize)
			assert.Equal(t, tt.want, got)
		})
	}
}
