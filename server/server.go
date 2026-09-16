// Package server exposes the chat pipeline over HTTP and serves the UI.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/optionalsoftware/ask-about/config"
	"github.com/optionalsoftware/ask-about/corpus"
	"github.com/optionalsoftware/ask-about/llm"
	"github.com/optionalsoftware/ask-about/pipeline"
	"github.com/optionalsoftware/ask-about/store"
)

// Features tells the client which optional surfaces are switched on, so the
// config file stays the single place they're controlled.
type Features struct {
	Avatar     bool   `json:"avatar"`
	Background string `json:"background"`
	// Name is the subject's full name, so the page title and header come
	// from config rather than being written into the HTML.
	Name string `json:"name"`
	// Tagline is the line under the title, also from config.
	Tagline string `json:"tagline"`
	// Questions are the starter prompts shown on an empty conversation.
	Questions []string `json:"questions"`
	// Disclaimer is the small print under the composer.
	Disclaimer string `json:"disclaimer"`
	// Photo reports whether an intro photograph is available to fetch. The
	// path itself stays server-side; the client only gets a yes or no.
	Photo bool `json:"photo"`
	// Admitted reports whether this visitor may ask anything. Per-request
	// rather than static config, so a turned-away visitor is told on arrival
	// instead of after typing a question.
	Admitted bool `json:"admitted"`
	// Denied is what to show them when they are not. Never says which of
	// expired, revoked or unknown applies.
	Denied string `json:"denied,omitempty"`
	// NoLink distinguishes "you arrived without a link" from "your link
	// stopped working". The first is the front door and reads as an
	// invitation; the second is a refusal.
	NoLink bool `json:"noLink,omitempty"`
}

type Server struct {
	pipe      *pipeline.Pipeline
	web       fs.FS
	features  Features
	photoPath string
	// store is nil when persistence is switched off, which also switches off
	// the admin pages — there would be nothing for them to read or write.
	store   store.Store
	pricing config.Pricing
	admin   config.Admin
	preview Preview
	// inviteOnly gates asking, never the page itself.
	inviteOnly bool
	deniedMsg  string
	// noLinkMsg is the front door: what someone sees who found the URL and
	// has no link at all.
	noLinkMsg     string
	maxPerSession float64
	maxPerDay     float64
	sessionCapMsg string
	dayCapMsg     string
	// favicon is the tab icon, rendered from the subject's name at startup.
	favicon []byte
	// dev serves web/ from disk, so index.html is reparsed per request.
	dev   bool
	index indexTemplate
	// prompts are the interview prompts for this subject's kind, loaded once.
	prompts []prompt
	product bool
	trusted *netip.Prefix
	// docs answers Current() on every question. Set from Options.Documents,
	// or a static wrapper around the corpus passed to New.
	docs      Documents
	adminAuth Authenticator
	// pages is the admin navigation and routes: the built-in pages, then
	// Options.AdminPages.
	pages []AdminPage
	log   *slog.Logger
}

type Options struct {
	Features Features
	// Subject is needed for the name placeholders in the access copy.
	Subject   config.Subject
	PhotoPath string
	Store     store.Store
	Pricing   config.Pricing
	Admin     config.Admin
	Preview   Preview
	Access    config.Access
	Dev       bool
	// Prompts holds the interview prompts, one file per entry in the
	// catalogue in document.go, for the admin page that hands them out.
	Prompts fs.FS
	// TrustedProxy is the only source X-Forwarded-For is believed from. Nil
	// means the connecting address is always the client.
	TrustedProxy *netip.Prefix

	// AdminAuth gates every admin page. Nil uses HTTP Basic auth with the
	// [admin] credentials, the lockout, and the address allowlist.
	AdminAuth Authenticator
	// AdminPages are mounted behind AdminAuth after the built-in Links and
	// Your Document pages, and listed in the admin navigation in order — or,
	// with ReplaceAdminPages, instead of them.
	AdminPages []AdminPage
	// ReplaceAdminPages drops the built-in admin pages so AdminPages is the
	// whole admin: a caller with its own admin mounts nothing of this one.
	ReplaceAdminPages bool
	// Documents supplies the document every answer comes from. Nil serves
	// the corpus passed to New for the life of the process.
	Documents Documents
}

// Authenticator decides who may reach the admin pages. Wrap returns a
// handler that either serves next or refuses.
type Authenticator interface {
	Wrap(next http.Handler) http.Handler
}

// AdminPage is one entry in the admin navigation and the routes behind it.
type AdminPage struct {
	// Title is the navigation label.
	Title string
	// Path is the page's path under Base + "/admin", starting with "/"; ""
	// is the admin root. The navigation links here.
	Path string
	// Routes are patterns relative to Base + "/admin", each with its method
	// ("GET /document/{slug}"). Every one is mounted behind AdminAuth.
	Routes map[string]http.Handler
}

// Documents supplies the current document. Current is called on every
// question, so an implementation may swap the document without a restart.
type Documents interface {
	Current() *corpus.Corpus
}

// static is the free edition's Documents: the corpus loaded at startup.
type static struct{ c *corpus.Corpus }

func (s static) Current() *corpus.Corpus { return s.c }

func New(pipe *pipeline.Pipeline, c *corpus.Corpus, web fs.FS, opts Options, log *slog.Logger) *Server {
	opts.Features.Photo = opts.PhotoPath != ""
	docs := opts.Documents
	if docs == nil {
		docs = static{c}
	}
	s := &Server{
		pipe: pipe, web: web,
		features: opts.Features, photoPath: opts.PhotoPath,
		store: opts.Store, pricing: opts.Pricing, admin: opts.Admin,
		preview:       opts.Preview,
		inviteOnly:    opts.Access.InviteOnly,
		deniedMsg:     opts.Access.DeniedMessage(),
		noLinkMsg:     opts.Access.NoLinkMessage(opts.Subject),
		maxPerSession: opts.Access.MaxCostPerSession,
		maxPerDay:     opts.Access.MaxCostPerDay,
		sessionCapMsg: opts.Access.SessionCapDenied(),
		dayCapMsg:     opts.Access.DayCapDenied(),
		favicon:       buildFavicon(opts.Subject),
		dev:           opts.Dev,
		prompts:       loadPrompts(opts.Prompts, opts.Subject.IsProduct(), log),
		product:       opts.Subject.IsProduct(),
		trusted:       opts.TrustedProxy,
		docs:          docs,
		adminAuth:     opts.AdminAuth,
		log:           log,
	}
	if opts.ReplaceAdminPages {
		s.pages = opts.AdminPages
	} else {
		s.pages = append(s.builtinPages(), opts.AdminPages...)
	}
	return s
}

// Base is the one prefix ask-about owns. Nothing it serves sits outside this.
//
// Not "/api": a site is likely to have one already, and the same goes for
// "/admin", "/app.js" and the rest. ask-about is meant to be able to share a domain
// without arguing over a common name, and a namespace with holes in it is not
// a namespace — so the page and the invite links live here too.
const Base = "/ask-about"

func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Base+"/chat", s.handleChat)
	mux.HandleFunc("GET "+Base+"/health", s.handleHealth)
	mux.HandleFunc("GET "+Base+"/config", s.handleConfig)
	mux.HandleFunc("GET "+Base+"/avatar-photo", s.handlePhoto)
	mux.HandleFunc("GET "+Base+"/favicon.svg", s.handleFavicon)
	mux.HandleFunc("GET "+previewImagePath, s.handlePreviewImage)
	if err := s.adminRoutes(mux, s.admin); err != nil {
		return nil, err
	}

	mux.HandleFunc("GET "+Base+"/i/{name}/{token}", s.handleInvite)

	// The page and the static assets. Anything under the prefix that is not a
	// real file 404s rather than falling back to the page: a typo in an asset
	// name should look like a typo, not like HTML the browser then fails to
	// parse as JavaScript.
	//
	// A request for Base with no trailing slash is redirected here by the mux.
	mux.Handle("GET "+Base+"/", http.StripPrefix(Base+"/", s.staticHandler()))
	return mux, nil
}

// handlePhoto serves the configured intro photograph. The path comes from
// config and is never taken from the request, so there is nothing here for a
// caller to traverse.
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	if s.photoPath == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, s.photoPath)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	features := s.features
	if ok := s.admit(r.Context(), r); ok.OK {
		features.Admitted = true
	} else {
		features.Denied = ok.Message
		features.NoLink = ok.Reason == reasonNoLink
		// No starter prompts for someone who cannot ask anything — offering
		// questions that will be refused is worse than offering none.
		features.Questions = nil
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(features)
}

type chatRequest struct {
	Messages []struct {
		Role string `json:"role"`
		Text string `json:"text"`
	} `json:"messages"`
}

// Bounds on what one turn may carry.
//
// The body limit alone is not a spend limit: a megabyte of text is roughly a
// quarter of a million tokens, which at input rates is most of a session's
// budget in a single uncached request. The caps are checked against spend so
// far, so they stop the *next* question — nothing bounds the current one but
// this.
const (
	// maxQuestionChars is about a thousand tokens. Longer than any question
	// anyone types, short enough that no single request can cost much.
	maxQuestionChars = 4000
	// historyTurns is how much of the conversation is replayed to the model.
	// Ten exchanges is far more than a visit runs to.
	historyTurns = 20
)

// handleChat streams the answer as Server-Sent Events.
//
// This is a POST rather than an EventSource GET because the client sends its
// question in the body.
//
// Only the question is taken from the client. The conversation it is a
// follow-up to is read back out of the store, because the browser's copy is
// not evidence of anything: a visitor can put whatever they like in an
// "assistant" turn and have the model treat its own words as established.
// This bot answers as a person, so a forged history is the difference between
// a quote and a fabrication.
//
// The cost of doing it this way is that a deployment with no storage has no
// history to read, and each question stands alone there.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	question := lastQuestion(req)
	if question == "" {
		http.Error(w, "messages is empty", http.StatusBadRequest)
		return
	}
	if len(question) > maxQuestionChars {
		http.Error(w, "that question is too long", http.StatusRequestEntityTooLarge)
		return
	}

	// Checked on every question rather than once on arrival, so revoking a
	// link stops the next question instead of the next visit. Before the
	// stream starts, so a refusal is a plain status code.
	adm := s.admit(r.Context(), r)
	if !adm.OK {
		s.log.Info("refused a question", "reason", adm.Reason, "remote", r.RemoteAddr)
		http.Error(w, adm.Message, http.StatusForbidden)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Before WriteHeader: Set-Cookie is a header, and the stream below starts
	// the body.
	session := s.sessionID(w, r)
	msgs := s.conversation(r.Context(), session, question)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // don't let a proxy buffer the stream
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// r.Context() is cancelled when the client disconnects, which propagates
	// through the pipeline and aborts the in-flight model request.
	events := s.pipe.Run(r.Context(), llm.Request{
		System:   s.docs.Current().System(),
		Messages: msgs,
		Session:  session,
		Invite:   adm.InviteID,
	})

	enc := json.NewEncoder(w)
	for ev := range events {
		if _, err := w.Write([]byte("data: ")); err != nil {
			return
		}
		if err := enc.Encode(ev); err != nil { // Encode writes the trailing \n
			return
		}
		if _, err := w.Write([]byte("\n")); err != nil { // blank line ends the event
			return
		}
		flusher.Flush()
	}
}

// lastQuestion pulls the new question out of the request: the final user
// message. The client still posts the whole conversation, and everything
// except this is ignored.
func lastQuestion(req chatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == string(llm.RoleAssistant) {
			continue
		}
		if q := strings.TrimSpace(req.Messages[i].Text); q != "" {
			return q
		}
	}
	return ""
}

// conversation builds the messages for one turn: this visit's earlier turns,
// as this server recorded them, followed by the new question.
//
// A store that errors costs the visitor their context, not their answer — a
// follow-up that reads oddly beats a refusal.
func (s *Server) conversation(ctx context.Context, session, question string) []llm.Message {
	var past []store.Turn
	if s.store != nil {
		var err error
		if past, err = s.store.TurnsForSession(ctx, session, historyTurns); err != nil {
			s.log.Error("could not read the conversation so far", "err", err)
		}
	}

	msgs := make([]llm.Message, 0, len(past)*2+1)
	for _, t := range past {
		msgs = append(msgs,
			llm.Message{Role: llm.RoleUser, Text: t.Question},
			llm.Message{Role: llm.RoleAssistant, Text: t.Answer},
		)
	}
	return append(msgs, llm.Message{Role: llm.RoleUser, Text: question})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// staticHandler serves the UI's assets. It is mounted under Base, so the path
// it sees has already had that prefix stripped.
func (s *Server) staticHandler() http.Handler {
	files := http.FileServer(http.FS(s.web))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		// index.html is rendered rather than served, so its preview metadata
		// is filled in before it leaves.
		if name == "" || name == "index.html" {
			s.serveIndex(w, r)
			return
		}
		if _, err := fs.Stat(s.web, name); err != nil {
			http.NotFound(w, r)
			return
		}

		// no-cache means "revalidate", not "don't store": the browser still
		// caches but checks first, and FileServer answers with a 304 when
		// nothing changed. Assets here have stable filenames and no content
		// hashing, so a max-age would pin stale JS and CSS with nothing able
		// to invalidate it — exactly the trap a build step normally prevents.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
