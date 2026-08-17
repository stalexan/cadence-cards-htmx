// Package samples exposes the ready-made topics bundled with the binary.
//
// A sample is one topic-format YAML file under web/samples. There is no
// manifest: the catalog is built by parsing the files themselves with the same
// yamlio.ImportTopic the /import route runs, so a sample's title, blurb and
// counts cannot drift from what importing it actually produces, and adding a
// file to the directory is the whole of adding a gallery entry.
//
// Samples are exported without SM-2 parameters on purpose. Every card arrives
// new and therefore due today, so the first study session after adding one is a
// full session rather than "nothing due, come back tomorrow".
package samples

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"cadence-cards/internal/yamlio"
	"cadence-cards/web"
)

// Sample is one bundled topic, parsed and ready to import.
type Sample struct {
	// Slug is the file's base name without .yaml. It is the URL segment, so it
	// is also the catalog's identity — renaming a file changes its URL.
	Slug string
	// Name and Description come from the topic's own config, not from any
	// separate metadata, so the gallery card shows what the import will create.
	Name        string
	Description string
	DeckCount   int
	CardCount   int
	// Topic is the parsed file. Handlers map it into store params rather than
	// re-parsing the YAML.
	Topic yamlio.ImportedTopic
	// YAML is the raw file, for the "preview" view.
	YAML string
}

var (
	once   sync.Once
	loaded []Sample
	loadEr error
)

// All returns the catalog in filename order, which is display order.
//
// Parsing happens once, lazily. A malformed sample is a build-time mistake
// rather than a runtime condition — TestCatalogLoads fails the build on one —
// so the error is surfaced to the caller instead of being tolerated per file:
// half a gallery is a worse answer than a visible failure.
func All() ([]Sample, error) {
	once.Do(func() { loaded, loadEr = load(web.Samples) })
	return loaded, loadEr
}

// Get returns one sample by slug. The second result is false when no such
// sample exists, which handlers turn into a 404.
func Get(slug string) (Sample, bool, error) {
	all, err := All()
	if err != nil {
		return Sample{}, false, err
	}
	for _, s := range all {
		if s.Slug == slug {
			return s, true, nil
		}
	}
	return Sample{}, false, nil
}

// load parses every .yaml under samples/ in the given FS. Split out from All so
// tests can point it at a fixture directory.
func load(fsys fs.FS) ([]Sample, error) {
	names, err := fs.Glob(fsys, "samples/*.yaml")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	out := make([]Sample, 0, len(names))
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		content := string(raw)

		if got := yamlio.Detect(content); got != yamlio.FormatTopic {
			return nil, fmt.Errorf("sample %s: not a topic file", name)
		}
		parsed, err := yamlio.ImportTopic(content)
		if err != nil {
			return nil, fmt.Errorf("sample %s: %w", name, err)
		}
		if len(parsed.Errors) > 0 {
			return nil, fmt.Errorf("sample %s: %s", name, strings.Join(parsed.Errors, "; "))
		}

		s := Sample{
			Slug:  strings.TrimSuffix(name[strings.LastIndex(name, "/")+1:], ".yaml"),
			Name:  parsed.Config.Name,
			Topic: parsed,
			YAML:  content,
		}
		if parsed.Config.TopicDescription != nil {
			s.Description = *parsed.Config.TopicDescription
		}
		for _, d := range parsed.Decks {
			if len(d.Invalid) > 0 {
				return nil, fmt.Errorf("sample %s: deck %q has %d unparseable card(s): %s",
					name, d.Name, len(d.Invalid), d.Invalid[0].Error)
			}
			s.DeckCount++
			s.CardCount += len(d.Cards)
		}
		out = append(out, s)
	}
	return out, nil
}
