package server

import (
	"slices"
	"testing"
)

// TestPageWindow pins PageWindow to visiblePages() in the Svelte app's
// ui/Pagination.svelte. 0 stands for an ellipsis. The <=7 shortcut and the
// cur>3 / cur<total-2 boundaries are the parts worth pinning: they decide
// whether an ellipsis appears next to the first/last page.
func TestPageWindow(t *testing.T) {
	tests := []struct {
		name  string
		page  int
		total int
		want  []int
	}{
		{"single page", 1, 1, []int{1}},
		{"exactly seven shows all", 4, 7, []int{1, 2, 3, 4, 5, 6, 7}},
		{"eight pages, near start", 1, 8, []int{1, 2, 0, 8}},
		{"eight pages, page 3 has no leading ellipsis", 3, 8, []int{1, 2, 3, 4, 0, 8}},
		{"eight pages, page 4 gains one", 4, 8, []int{1, 0, 3, 4, 5, 0, 8}},
		{"eight pages, near end", 8, 8, []int{1, 0, 7, 8}},
		{"twenty pages, middle", 10, 20, []int{1, 0, 9, 10, 11, 0, 20}},
		{"twenty pages, second to last", 19, 20, []int{1, 0, 18, 19, 20}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := cardTableData{Page: tt.page, TotalPages: tt.total}
			if got := d.PageWindow(); !slices.Equal(got, tt.want) {
				t.Errorf("PageWindow(page=%d, total=%d) = %v, want %v", tt.page, tt.total, got, tt.want)
			}
		})
	}
}

func TestStartEndItem(t *testing.T) {
	tests := []struct {
		name               string
		page, total        int
		wantStart, wantEnd int
	}{
		{"empty result set", 1, 0, 0, 0},
		{"first page, partial", 1, 12, 1, 12},
		{"first page, full", 1, 100, 1, cardsPerPage},
		{"last page is clamped to the total", 3, 55, 2*cardsPerPage + 1, 55},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := cardTableData{Page: tt.page, Total: tt.total}
			if got := d.StartItem(); got != tt.wantStart {
				t.Errorf("StartItem() = %d, want %d", got, tt.wantStart)
			}
			if got := d.EndItem(); got != tt.wantEnd {
				t.Errorf("EndItem() = %d, want %d", got, tt.wantEnd)
			}
		})
	}
}
