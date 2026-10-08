package postgres

import (
	"reflect"
	"testing"
)

func TestParseBlockRefBlocks(t *testing.T) {
	cases := []struct {
		name        string
		blockRef    string
		relfilenode uint32
		want        []int
	}{
		{
			name:        "single main-fork block, matching relfilenode",
			blockRef:    "blkref #0: rel 1663/16401/24595 fork main blk 3",
			relfilenode: 24595,
			want:        []int{3},
		},
		{
			name:        "non-main fork is ignored",
			blockRef:    "blkref #0: rel 1663/16401/24595 fork vm blk 0",
			relfilenode: 24595,
			want:        nil,
		},
		{
			name:        "different relfilenode is ignored",
			blockRef:    "blkref #0: rel 1663/16401/99999 fork main blk 3",
			relfilenode: 24595,
			want:        nil,
		},
		{
			name: "multiple blkref lines, mixed matches",
			blockRef: "blkref #0: rel 1663/16401/24595 fork main blk 1\n" +
				"blkref #1: rel 1663/16401/24595 fork main blk 2\n" +
				"blkref #2: rel 1663/16401/777 fork main blk 0",
			relfilenode: 24595,
			want:        []int{1, 2},
		},
		{
			name:        "full page write suffix still parses the block number",
			blockRef:    "blkref #0: rel 1663/16401/24595 fork main blk 5 FPW",
			relfilenode: 24595,
			want:        []int{5},
		},
		{
			name:        "empty string yields no blocks",
			blockRef:    "",
			relfilenode: 24595,
			want:        nil,
		},
		{
			name:        "malformed text yields no blocks",
			blockRef:    "not a blkref line at all",
			relfilenode: 24595,
			want:        nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBlockRefBlocks(tc.blockRef, tc.relfilenode)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseBlockRefBlocks(%q, %d) = %v, want %v",
					tc.blockRef, tc.relfilenode, got, tc.want)
			}
		})
	}
}
