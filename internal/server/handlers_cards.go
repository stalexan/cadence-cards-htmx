package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cadence-cards/internal/claude"
	"cadence-cards/internal/sm2"
	"cadence-cards/internal/store"
)

const cardsPerPage = 25 // matches the Svelte cards page

// cardTableData feeds the shared card_table.html fragment (cards list page
// and deck detail).
type cardTableData struct {
	Cards      []store.Card
	Total      int
	Page       int
	TotalPages int
	Now        time.Time

	// Current filter state (echoed into the filter controls and pagination).
	Query    store.CardListParams
	DeckID   *int64 // deck-scoped table (deck detail page) when non-nil
	Topics   []store.Topic
	Decks    []store.Deck
	Tags     []string
	BasePath string // hx-get target: /cards/table or /decks/{id}/cards
	PushURL  string // hx-push-url base: /cards or /decks/{id}
}

// cardTable builds table data from query params. deckID non-nil scopes the
// table to one deck (deck detail page).
func (s *Server) cardTable(r *http.Request, deckID *int64) (cardTableData, error) {
	userID := userFrom(r).ID
	q := r.URL.Query()

	params := store.CardListParams{
		Search:   q.Get("q"),
		Priority: q.Get("priority"),
		Tag:      q.Get("tag"),
		Sort:     q.Get("sort"),
		Dir:      q.Get("dir"),
		Page:     1,
		PerPage:  cardsPerPage,
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		params.Page = p
	}
	if deckID != nil {
		params.DeckID = deckID
	} else {
		params.TopicID = queryInt64Ptr(r, "topicId")
		params.DeckID = queryInt64Ptr(r, "deckId")
	}
	if params.Priority != "" && !sm2.ValidPriority(params.Priority) {
		params.Priority = ""
	}

	cards, total, err := s.store.ListCards(r.Context(), userID, params)
	if err != nil {
		return cardTableData{}, err
	}

	data := cardTableData{
		Cards: cards,
		Total: total,
		Page:  params.Page,
		Now:   time.Now(),
		Query: params,
	}
	data.TotalPages = (total + cardsPerPage - 1) / cardsPerPage
	if data.TotalPages == 0 {
		data.TotalPages = 1
	}

	if deckID != nil {
		data.DeckID = deckID
		data.BasePath = "/decks/" + itoa(*deckID) + "/cards"
		data.PushURL = "/decks/" + itoa(*deckID)
	} else {
		data.BasePath = "/cards/table"
		data.PushURL = "/cards"
		// Filter dropdown data (full cards page only).
		if data.Topics, err = s.store.ListTopics(r.Context(), userID); err != nil {
			return cardTableData{}, err
		}
		if data.Decks, err = s.store.ListDecks(r.Context(), userID, nil); err != nil {
			return cardTableData{}, err
		}
		if data.Tags, err = s.store.DistinctTags(r.Context(), userID); err != nil {
			return cardTableData{}, err
		}
	}
	return data, nil
}

// StartItem and EndItem are the 1-based bounds of the current page, for the
// "Showing X to Y of Z results" summary.
func (d cardTableData) StartItem() int {
	if d.Total == 0 {
		return 0
	}
	return (d.Page-1)*cardsPerPage + 1
}

func (d cardTableData) EndItem() int {
	return min(d.Page*cardsPerPage, d.Total)
}

// PageWindow returns the page buttons to render. A positive value is a page
// number; 0 means an ellipsis. Direct port of visiblePages() in the reference's
// ui/Pagination.svelte — keep the boundaries identical, they are load-bearing
// for which pages stay visible near the ends.
func (d cardTableData) PageWindow() []int {
	total, cur := d.TotalPages, d.Page
	if total <= 7 {
		pages := make([]int, 0, total)
		for i := 1; i <= total; i++ {
			pages = append(pages, i)
		}
		return pages
	}

	pages := []int{1}
	if cur > 3 {
		pages = append(pages, 0)
	}
	for i := max(2, cur-1); i <= min(total-1, cur+1); i++ {
		pages = append(pages, i)
	}
	if cur < total-2 {
		pages = append(pages, 0)
	}
	return append(pages, total)
}

// QueryString rebuilds the current filter query with an overridden key
// (pagination links, sort toggles).
func (d cardTableData) QueryString(overrides ...string) string {
	v := url.Values{}
	set := func(key, val string) {
		if val != "" {
			v.Set(key, val)
		}
	}
	set("q", d.Query.Search)
	if d.DeckID == nil {
		if d.Query.TopicID != nil {
			set("topicId", itoa(*d.Query.TopicID))
		}
		if d.Query.DeckID != nil {
			set("deckId", itoa(*d.Query.DeckID))
		}
	}
	set("priority", d.Query.Priority)
	set("tag", d.Query.Tag)
	set("sort", d.Query.Sort)
	set("dir", d.Query.Dir)
	if d.Page > 1 {
		set("page", strconv.Itoa(d.Page))
	}
	for i := 0; i+1 < len(overrides); i += 2 {
		if overrides[i+1] == "" {
			v.Del(overrides[i])
		} else {
			v.Set(overrides[i], overrides[i+1])
		}
	}
	if enc := v.Encode(); enc != "" {
		return "?" + enc
	}
	return ""
}

// SortLink returns the query string that sorts by col, toggling direction
// when already active.
func (d cardTableData) SortLink(col string) string {
	dir := "asc"
	if d.Query.Sort == col && d.Query.Dir != "desc" {
		dir = "desc"
	}
	return d.QueryString("sort", col, "dir", dir, "page", "")
}

func (s *Server) handleCardsList(w http.ResponseWriter, r *http.Request) {
	table, err := s.cardTable(r, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "cards_list", table)
}

func (s *Server) handleCardsTableFragment(w http.ResponseWriter, r *http.Request) {
	table, err := s.cardTable(r, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The server, not each control, decides the pushed URL: it reflects the
	// filter state that was actually rendered, so refresh/back round-trip.
	w.Header().Set("HX-Push-Url", table.PushURL+table.QueryString())
	s.fragment(w, http.StatusOK, "card_table", table)
}

func (s *Server) handleDeckCardsFragment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Ownership check before rendering the deck-scoped table.
	if _, err := s.store.GetDeck(r.Context(), userFrom(r).ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	table, err := s.cardTable(r, &id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("HX-Push-Url", table.PushURL+table.QueryString())
	s.fragment(w, http.StatusOK, "card_table", table)
}

func cardParamsFromForm(r *http.Request) store.CardParams {
	deckID, _ := strconv.ParseInt(formStr(r, "deckId"), 10, 64)
	return store.CardParams{
		DeckID:   deckID,
		Front:    formStr(r, "front"),
		Back:     formStr(r, "back"),
		Note:     formStrPtr(r, "note"),
		Priority: sm2.Priority(formStr(r, "priority")),
		Tags:     parseTags(r.FormValue("tags")),
	}
}

// cardFormData feeds cards_form.html (create) and cards_show.html (edit).
type cardFormData struct {
	Card            *store.Card
	Topics          []store.Topic
	Decks           []store.Deck
	Params          store.CardParams
	Error           string
	VersionConflict bool
	Now             time.Time
	// Editing switches cards_show from the read-only view to the edit form.
	// The reference toggles this in place; here it is a plain ?edit=1
	// navigation, so the two modes stay separately linkable.
	Editing bool
}

// SelectedTopicID is the topic owning the currently selected deck, so the
// topic select can be pre-set when the form re-renders after a validation
// error. 0 when nothing is selected yet.
func (d cardFormData) SelectedTopicID() int64 {
	for _, deck := range d.Decks {
		if deck.ID == d.Params.DeckID {
			return deck.TopicID
		}
	}
	return 0
}

// DecksForSelectedTopic narrows the deck list to the selected topic. With no
// topic chosen the deck select starts empty, matching the reference.
func (d cardFormData) DecksForSelectedTopic() []store.Deck {
	topicID := d.SelectedTopicID()
	if topicID == 0 {
		return nil
	}
	var out []store.Deck
	for _, deck := range d.Decks {
		if deck.TopicID == topicID {
			out = append(out, deck)
		}
	}
	return out
}

// cardContentData feeds the card_content_fields partial: the assist box plus
// the front/back/note inputs, the region POST /cards/assist re-renders.
type cardContentData struct {
	Front string
	Back  string
	Note  string
	// Instruction is echoed back on failure so a retry costs nothing, and
	// cleared on success: the next press of an iterative feature carries a new
	// instruction, and echoing the old one risks re-applying it.
	Instruction string
	Notice      string
	Error       string
}

// ContentFields adapts the form's two data sources to the shared partial: the
// edit page renders from the card, the create page from the typed params.
func (d cardFormData) ContentFields() cardContentData {
	if d.Editing && d.Card != nil {
		c := cardContentData{Front: d.Card.Front, Back: d.Card.Back}
		if d.Card.Note != nil {
			c.Note = *d.Card.Note
		}
		return c
	}
	c := cardContentData{Front: d.Params.Front, Back: d.Params.Back}
	if d.Params.Note != nil {
		c.Note = *d.Params.Note
	}
	return c
}

// handleCardDeckOptions re-renders just the deck <select> options when the
// topic select changes. The reference filters this list client-side; doing it
// server-side keeps the page free of custom JS under the strict CSP.
func (s *Server) handleCardDeckOptions(w http.ResponseWriter, r *http.Request) {
	userID := userFrom(r).ID
	decks, err := s.store.ListDecks(r.Context(), userID, queryInt64Ptr(r, "topicId"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.fragment(w, http.StatusOK, "deck_options", deckOptionsData{
		Decks:    decks,
		Selected: r.URL.Query().Get("deckId"),
	})
}

// deckOptionsData feeds the deck_options partial.
type deckOptionsData struct {
	Decks    []store.Deck
	Selected string
}

// maxPreviewField bounds one field of a preview request. Generous next to any
// real card, but it keeps a paste-bomb from turning into a large render.
const maxPreviewField = 64 << 10

// cardPreviewData feeds the card_preview partial: the three markdown fields as
// the author currently has them typed. The labels are static rather than the
// deck's Field1Label/Field2Label — the preview exists to check formatting, and
// resolving a deck would put a DB round-trip on a debounced keystroke path.
type cardPreviewData struct {
	Front string
	Back  string
	Note  string
	// Empty is true when all three fields are blank, so the panel can say so
	// instead of rendering three empty boxes.
	Empty bool
}

// handleCardPreview renders the card form's current front/back/note as
// markdown. It runs the same markdown.Render the card and study pages use, so
// the preview cannot drift from what saving actually produces — the same
// reason POST /import/detect runs the real parsers rather than sniffing the
// text in JavaScript. A pure render: no store access, and like the other live
// hint endpoints it answers 200 in every branch so htmx always swaps.
func (s *Server) handleCardPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*maxPreviewField)
	if err := r.ParseForm(); err != nil {
		s.fragment(w, http.StatusOK, "card_preview", cardPreviewData{Empty: true})
		return
	}
	data := cardPreviewData{
		Front: clampField(r.FormValue("front")),
		Back:  clampField(r.FormValue("back")),
		Note:  clampField(r.FormValue("note")),
	}
	data.Empty = strings.TrimSpace(data.Front) == "" &&
		strings.TrimSpace(data.Back) == "" &&
		strings.TrimSpace(data.Note) == ""
	s.fragment(w, http.StatusOK, "card_preview", data)
}

// clampField cuts an over-long preview field at a rune boundary.
func clampField(s string) string {
	if len(s) <= maxPreviewField {
		return s
	}
	r := []rune(s)
	if len(r) > maxPreviewField {
		r = r[:maxPreviewField]
	}
	return string(r)
}

// handleCardAssist drafts or revises the card's front/back/note from a
// one-line instruction. Unlike topic suggest it may overwrite filled fields —
// that is the point: the user iterates by sending new instructions against
// the current draft. Like the other in-form AI endpoints it answers 200 in
// every branch (htmx only swaps 2xx/409/422) and renders failures as bubbles.
func (s *Server) handleCardAssist(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*maxPreviewField)
	d := cardContentData{
		Front:       r.FormValue("front"),
		Back:        r.FormValue("back"),
		Note:        r.FormValue("note"),
		Instruction: formStr(r, "instruction"),
	}
	if d.Instruction == "" {
		d.Error = "Tell Claude what you want first — a few words is enough."
		s.fragment(w, http.StatusOK, "card_content_fields", d)
		return
	}

	rev, err := s.ai.AssistCard(r.Context(), s.cardAssistContext(r),
		claude.CardDraft{Front: d.Front, Back: d.Back, Note: d.Note}, d.Instruction)
	if err != nil {
		d.Error = aiErrorMessage(err)
		s.fragment(w, http.StatusOK, "card_content_fields", d)
		return
	}

	changed := applyRevision(&d, rev)
	d.Notice = plural(changed, "field", "fields") + " updated. Review below, then save."
	d.Instruction = ""
	s.fragment(w, http.StatusOK, "card_content_fields", d)
}

// applyRevision overwrites the fields the revision carries and counts them.
// Front and back only ever change to non-empty values (the parser already
// guarantees this; the check here is belt and braces), while a non-nil empty
// note is a deliberate clear.
func applyRevision(d *cardContentData, rev claude.CardRevision) int {
	changed := 0
	if rev.Front != nil && *rev.Front != "" {
		d.Front = *rev.Front
		changed++
	}
	if rev.Back != nil && *rev.Back != "" {
		d.Back = *rev.Back
		changed++
	}
	if rev.Note != nil {
		d.Note = *rev.Note
		changed++
	}
	return changed
}

// cardAssistContext resolves prompt context from the form's deck (preferred)
// or topic selection. Everything is fetched server-side by ID with the
// requester's userID — the browser never supplies topic or deck text — so a
// foreign ID is just ErrNotFound and, like every other failure here, degrades
// to the zero value: assistance without context beats no assistance.
func (s *Server) cardAssistContext(r *http.Request) claude.CardAssistContext {
	userID := userFrom(r).ID
	if deckID, _ := strconv.ParseInt(formStr(r, "deckId"), 10, 64); deckID > 0 {
		if deck, err := s.store.GetDeck(r.Context(), userID, deckID); err == nil {
			front, back := deck.FieldLabels()
			actx := claude.CardAssistContext{TopicName: deck.TopicName, FrontLabel: front, BackLabel: back}
			if topic, err := s.store.GetTopic(r.Context(), userID, deck.TopicID); err == nil && topic.TopicDescription != nil {
				actx.TopicDesc = *topic.TopicDescription
			}
			return actx
		}
	}
	if topicID, _ := strconv.ParseInt(formStr(r, "topicId"), 10, 64); topicID > 0 {
		if topic, err := s.store.GetTopic(r.Context(), userID, topicID); err == nil {
			actx := claude.CardAssistContext{TopicName: topic.Name}
			if topic.TopicDescription != nil {
				actx.TopicDesc = *topic.TopicDescription
			}
			return actx
		}
	}
	return claude.CardAssistContext{}
}

func (s *Server) cardFormData(r *http.Request, card *store.Card, params store.CardParams, errMsg string) (cardFormData, error) {
	userID := userFrom(r).ID
	topics, err := s.store.ListTopics(r.Context(), userID)
	if err != nil {
		return cardFormData{}, err
	}
	decks, err := s.store.ListDecks(r.Context(), userID, nil)
	if err != nil {
		return cardFormData{}, err
	}
	return cardFormData{Card: card, Topics: topics, Decks: decks, Params: params, Error: errMsg, Now: time.Now()}, nil
}

func (s *Server) handleCardNewPage(w http.ResponseWriter, r *http.Request) {
	params := store.CardParams{Priority: sm2.PriorityB}
	if did := queryInt64Ptr(r, "deckId"); did != nil {
		params.DeckID = *did
	}
	data, err := s.cardFormData(r, nil, params, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "cards_form", data)
}

func (s *Server) handleCardCreate(w http.ResponseWriter, r *http.Request) {
	p := cardParamsFromForm(r)
	renderErr := func(status int, msg string) {
		data, err := s.cardFormData(r, nil, p, msg)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.render(w, r, status, "cards_form", data)
	}
	if p.Front == "" || p.Back == "" || p.DeckID == 0 {
		renderErr(http.StatusBadRequest, "Front, back, and deck are required.")
		return
	}
	if !sm2.ValidPriority(string(p.Priority)) {
		renderErr(http.StatusBadRequest, "Priority must be A, B, or C.")
		return
	}
	card, err := s.store.CreateCard(r.Context(), userFrom(r).ID, p)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			renderErr(http.StatusBadRequest, "Deck not found.")
			return
		}
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cards/"+itoa(card.ID), http.StatusSeeOther)
}

func (s *Server) handleCardShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	card, err := s.store.GetCard(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	data, err := s.cardFormData(r, &card, store.CardParams{}, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Editing = r.URL.Query().Get("edit") != ""
	s.render(w, r, http.StatusOK, "cards_show", data)
}

func (s *Server) handleCardUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	userID := userFrom(r).ID
	p := cardParamsFromForm(r)
	version := formInt(r, "version", -1)

	renderForm := func(status int, msg string, conflict bool) {
		card, err := s.store.GetCard(r.Context(), userID, id)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		// Stay in edit mode and keep the user's typed input: the fresh card
		// supplies the current version (so a save after a 409 succeeds), the
		// submitted params supply the content.
		card.Front, card.Back, card.Note, card.Tags = p.Front, p.Back, p.Note, p.Tags
		if sm2.ValidPriority(string(p.Priority)) {
			card.Priority = p.Priority
		}
		if p.DeckID != 0 {
			card.DeckID = p.DeckID
		}
		data, err := s.cardFormData(r, &card, p, msg)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Editing = true
		data.VersionConflict = conflict
		s.render(w, r, status, "cards_show", data)
	}

	if p.Front == "" || p.Back == "" || p.DeckID == 0 || !sm2.ValidPriority(string(p.Priority)) || version < 0 {
		renderForm(http.StatusBadRequest, "Front, back, deck, and priority are required.", false)
		return
	}
	if _, err := s.store.UpdateCard(r.Context(), userID, id, version, p); err != nil {
		switch {
		case errors.Is(err, store.ErrVersionConflict):
			// Re-render with fresh data and a conflict banner.
			renderForm(http.StatusConflict, "", true)
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		default:
			s.serverError(w, r, err)
		}
		return
	}
	http.Redirect(w, r, "/cards/"+itoa(id), http.StatusSeeOther)
}

func (s *Server) handleCardDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteCard(r.Context(), userFrom(r).ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	// Return where the delete came from (deck page or cards list). Only
	// same-origin paths: "//host" and "/\host" are protocol-relative
	// redirects in browsers, so require exactly one leading slash.
	dest := r.FormValue("redirect")
	if dest == "" || dest[0] != '/' ||
		strings.HasPrefix(dest, "//") || strings.HasPrefix(dest, "/\\") {
		dest = "/cards"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleScheduleReset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	sched, err := s.store.ResetProgress(r.Context(), userFrom(r).ID, id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cards/"+itoa(sched.CardID), http.StatusSeeOther)
}
