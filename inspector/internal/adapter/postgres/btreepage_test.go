package postgres

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

func metaFunc(root, level int) func(context.Context) (int, int, error) {
	return func(context.Context) (int, int, error) { return root, level, nil }
}

func pageFunc(pages map[int][]IndexItem) func(context.Context, int) ([]IndexItem, error) {
	return func(_ context.Context, blk int) ([]IndexItem, error) {
		items, ok := pages[blk]
		if !ok {
			return nil, errors.New("no such page")
		}
		return items, nil
	}
}

func TestWalkIndexSingleLevelTreeRootIsLeaf(t *testing.T) {
	leafItems := []IndexItem{
		{ItemOffset: 1, CTID: "(0,1)", DataHex: "aa", Dead: false},
		{ItemOffset: 2, CTID: "(0,2)", DataHex: "bb", Dead: true},
	}
	got, err := walkIndex(context.Background(), "idx",
		metaFunc(1, 0),
		pageFunc(map[int][]IndexItem{1: leafItems}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("want 1 page, got %d", len(got.Pages))
	}
	if got.Pages[0].Type != "root" {
		t.Errorf("want type %q, got %q", "root", got.Pages[0].Type)
	}
	if got.Pages[0].Level != 0 {
		t.Errorf("want level 0, got %d", got.Pages[0].Level)
	}
	if got.Truncated {
		t.Error("want not truncated")
	}
}

func TestWalkIndexTwoLevelTreeDescendsToLeaves(t *testing.T) {
	root := []IndexItem{
		{ItemOffset: 1, CTID: "(2,0)", DataHex: "aa"},
		{ItemOffset: 2, CTID: "(3,0)", DataHex: "bb"},
	}
	leaf2 := []IndexItem{{ItemOffset: 1, CTID: "(0,1)", DataHex: "a1"}}
	leaf3 := []IndexItem{{ItemOffset: 1, CTID: "(0,5)", DataHex: "b1"}}

	got, err := walkIndex(context.Background(), "idx",
		metaFunc(1, 1),
		pageFunc(map[int][]IndexItem{1: root, 2: leaf2, 3: leaf3}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != 3 {
		t.Fatalf("want 3 pages, got %d", len(got.Pages))
	}
	if got.Pages[0].Block != 1 || got.Pages[0].Type != "root" {
		t.Errorf("page 0: want root block 1, got %+v", got.Pages[0])
	}
	if got.Pages[1].Block != 2 || got.Pages[1].Type != "leaf" || got.Pages[1].Level != 0 {
		t.Errorf("page 1: want leaf block 2 level 0, got %+v", got.Pages[1])
	}
	if got.Pages[2].Block != 3 || got.Pages[2].Type != "leaf" {
		t.Errorf("page 2: want leaf block 3, got %+v", got.Pages[2])
	}
	if got.Truncated {
		t.Error("want not truncated")
	}
}

func TestWalkIndexTruncatesWhenChildrenExceedCap(t *testing.T) {
	// Root has 15 children (four more than fit once the root itself is
	// counted against the 12-page cap: 1 root + 11 leaves = 12).
	root := make([]IndexItem, 15)
	pages := map[int][]IndexItem{}
	for i := range root {
		child := i + 2
		root[i] = IndexItem{ItemOffset: i + 1, CTID: "(" + strconv.Itoa(child) + ",0)"}
		pages[child] = []IndexItem{{ItemOffset: 1, CTID: "(0,1)"}}
	}
	pages[1] = root

	got, err := walkIndex(context.Background(), "idx", metaFunc(1, 1), pageFunc(pages))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != maxIndexPages {
		t.Fatalf("want %d pages, got %d", maxIndexPages, len(got.Pages))
	}
	if !got.Truncated {
		t.Error("want truncated")
	}
}

func TestWalkIndexExactlyFillingCapIsNotTruncated(t *testing.T) {
	// Root has 11 children: 1 root + 11 leaves = 12, exactly the cap.
	root := make([]IndexItem, 11)
	pages := map[int][]IndexItem{}
	for i := range root {
		child := i + 2
		root[i] = IndexItem{ItemOffset: i + 1, CTID: "(" + strconv.Itoa(child) + ",0)"}
		pages[child] = []IndexItem{{ItemOffset: 1, CTID: "(0,1)"}}
	}
	pages[1] = root

	got, err := walkIndex(context.Background(), "idx", metaFunc(1, 1), pageFunc(pages))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pages) != maxIndexPages {
		t.Fatalf("want %d pages, got %d", maxIndexPages, len(got.Pages))
	}
	if got.Truncated {
		t.Error("want not truncated: cap exactly filled")
	}
}

func TestWalkIndexRejectsMalformedCTID(t *testing.T) {
	root := []IndexItem{{ItemOffset: 1, CTID: "not-a-ctid"}}
	_, err := walkIndex(context.Background(), "idx",
		metaFunc(1, 1),
		pageFunc(map[int][]IndexItem{1: root}))
	if err == nil {
		t.Fatal("want error for malformed ctid")
	}
}
