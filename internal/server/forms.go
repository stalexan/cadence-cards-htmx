package server

import (
	"net/http"
	"strconv"
	"strings"
)

// Form parsing helpers. All POSTs are form-encoded; repeated params (deckIds)
// are read via r.Form[...] so every value is honored.

// formStr returns a trimmed form value.
func formStr(r *http.Request, key string) string {
	return strings.TrimSpace(r.FormValue(key))
}

// formStrPtr returns nil for an empty trimmed value, else a pointer to it.
func formStrPtr(r *http.Request, key string) *string {
	v := formStr(r, key)
	if v == "" {
		return nil
	}
	return &v
}

// formInt parses an int form value, returning fallback when absent/invalid.
func formInt(r *http.Request, key string, fallback int) int {
	v := formStr(r, key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// pathID parses the {id}-style path segment as int64.
func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// queryInt64Ptr parses an optional int64 query param.
func queryInt64Ptr(r *http.Request, key string) *int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return nil
	}
	return &n
}

// queryInt64s collects every repeated int64 query value for key.
func queryInt64s(r *http.Request, key string) []int64 {
	var out []int64
	for _, v := range r.URL.Query()[key] {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// parseTags splits a comma-separated hidden tags input into trimmed,
// de-duplicated tags.
func parseTags(raw string) []string {
	seen := map[string]bool{}
	tags := []string{}
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		tags = append(tags, t)
	}
	return tags
}
