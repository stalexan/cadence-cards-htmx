package server

import (
	"errors"
	"net/http"

	"cadence-cards/internal/samples"
	"cadence-cards/internal/store"
)

// samplesPageData feeds samples.html.
type samplesPageData struct {
	Samples []samples.Sample
}

func (s *Server) handleSamplesList(w http.ResponseWriter, r *http.Request) {
	all, err := samples.All()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "samples", samplesPageData{Samples: all})
}

// handleSamplePreview serves a sample's raw YAML as text, for the "preview"
// disclosure on the gallery. Plain text, never HTML: the body is card content
// like "Vector<int>" that an HTML parse would mangle, the same reason the
// export dialogs swap with textContent.
func (s *Server) handleSamplePreview(w http.ResponseWriter, r *http.Request) {
	sample, ok, err := samples.Get(r.PathValue("slug"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(sample.YAML))
}

// handleSampleImport adds a bundled sample to the user's account.
//
// It is the topic branch of POST /import with the YAML coming from the binary
// instead of a textarea: same parse, same store.ImportTopic, same result
// fragment. Adding a sample twice is therefore not an error — the store
// suffixes the topic name, and the fragment explains that it did.
func (s *Server) handleSampleImport(w http.ResponseWriter, r *http.Request) {
	sample, ok, err := samples.Get(r.PathValue("slug"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		// A 422 rather than a 404: this response is swapped into the page, and
		// htmx discards 404 bodies, which would leave a dead button.
		s.fragment(w, http.StatusUnprocessableEntity, "import_result",
			importResultData{Message: "That sample is no longer available."})
		return
	}

	result := importResultData{IsTopic: true}
	p := topicImportParams(sample.Topic, &result)

	res, err := s.store.ImportTopic(r.Context(), userFrom(r).ID, p)
	if err != nil {
		if errors.Is(err, store.ErrDuplicate) {
			s.fragment(w, http.StatusUnprocessableEntity, "import_result",
				importResultData{Message: "Could not find an available name for this topic."})
			return
		}
		s.storeError(w, r, err)
		return
	}

	result.Success = true
	result.Message = "Sample added."
	result.TopicID = res.TopicID
	result.TopicName = res.TopicName
	result.TopicRenamed = res.Renamed
	result.DeckCount = res.DeckCount
	result.ImportedCount = res.CardCount
	result.Errors = append(result.Errors, res.DeckRenames...)
	s.fragment(w, http.StatusOK, "import_result", result)
}
