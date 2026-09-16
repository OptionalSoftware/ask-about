// Package config loads ask-about's TOML configuration.
//
// Every field has a default, and callers may pass an embedded fallback so the
// binary runs with no config file present at all.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/optionalsoftware/ask-about/internal/subject"
)

type Config struct {
	Server  Server  `toml:"server"`
	Subject Subject `toml:"subject"`
	Corpus  Corpus  `toml:"corpus"`
	LLM     LLM     `toml:"llm"`
	Storage Storage `toml:"storage"`
	Admin   Admin   `toml:"admin"`
	Access  Access  `toml:"access"`
	Pricing Pricing `toml:"pricing"`
	Avatar  Avatar  `toml:"avatar"`
	Preview Preview `toml:"preview"`

	// Unknown holds keys the file set that nothing reads — a typo, or a
	// setting from a newer version. Not a toml field itself.
	Unknown []string `toml:"-"`
}

type Server struct {
	Addr string `toml:"addr"`
	// TrustedProxy is the address, or CIDR block, of the reverse proxy in
	// front of the server. X-Forwarded-For is believed only on connections
	// from it. Empty means no proxy: the connecting address is the client,
	// and the header is ignored.
	TrustedProxy string `toml:"trusted_proxy"`
}

// TrustedNet parses TrustedProxy. Returns ok=false when none is configured.
func (s Server) TrustedNet() (netip.Prefix, bool, error) {
	return parseNet("server.trusted_proxy", s.TrustedProxy)
}

// Subject is what the corpus is about — a person, or a product. The persona
// and the page refer to it by name and by pronoun, so both live here rather
// than being written into either.
type Subject struct {
	// Kind is "person" (the default), "product", or "company". The last two
	// are the same thing to the code — a subject that is not a person, with
	// the product persona and it/its — and both are accepted because people
	// write whichever describes them.
	Kind string `toml:"kind"`
	// Name is the whole name of a subject that is not a person. When set it
	// stands in for firstName and lastName everywhere: {{fullName}} and
	// {{firstName}} both render it, and {{lastName}} renders nothing.
	Name      string `toml:"name"`
	FirstName string `toml:"firstName"`
	LastName  string `toml:"lastName"`
	// Pronouns is one of "he/him", "she/her", "they/them" or "it/its". Empty
	// means they/them for a person and it/its for a product. The persona carries pronoun placeholders rather
	// than one subject's pronouns, so this is what stops a clone of this repo
	// from misgendering its own subject.
	Pronouns string `toml:"pronouns"`
	// Tagline is the line under the title. Shown to visitors, not sent to the
	// model — it sets expectations about what the bot will answer.
	Tagline string `toml:"tagline"`
	// Disclaimer is the small print under the composer. Unset falls back to a
	// short default; set it to say more. Accepts the same placeholders the
	// persona does — {{firstName}}, {{lastName}}, {{fullName}} and the pronoun
	// family — so the copy stays portable to a different subject.
	Disclaimer string `toml:"disclaimer"`
	// Questions are the starter prompts offered on an empty conversation.
	// Optional: unset falls back to corpus-agnostic defaults. More than
	// maxQuestions are truncated — a wall of chips stops being a suggestion.
	Questions []string `toml:"questions"`
}

const (
	KindPerson  = "person"
	KindProduct = "product"
	KindCompany = "company"
)

// IsProduct reports whether the subject is something other than a person.
func (s Subject) IsProduct() bool { return s.Kind == KindProduct || s.Kind == KindCompany }

// KindName is the kind with the default spelled out, for logs and messages.
func (s Subject) KindName() string {
	if s.Kind == "" {
		return KindPerson
	}
	return s.Kind
}

// PersonaFile is the built-in persona for this kind of subject, as a path
// under prompts/. It doubles as the default disk path, so editing the file in
// a checkout takes effect without a rebuild.
func (s Subject) PersonaFile() string {
	if s.IsProduct() {
		return "prompts/product.md"
	}
	return "prompts/person.md"
}

// maxQuestions caps the starter prompts. Six fits two rows at most widths;
// beyond that they stop reading as a short menu.
const maxQuestions = 6

// StarterQuestions returns the configured prompts, or generic fallbacks when
// none are set. The defaults name nothing and assume no gender, so they suit
// whatever corpus the binary is pointed at.
func (s Subject) StarterQuestions() []string {
	qs := make([]string, 0, maxQuestions)
	for _, q := range s.Questions {
		if strings.TrimSpace(q) != "" {
			qs = append(qs, strings.TrimSpace(q))
		}
	}
	if len(qs) == 0 {
		if s.IsProduct() {
			return []string{
				"Give me a quick overview.",
				"What does it do?",
				"Who is it for?",
				"What does it cost?",
			}
		}
		return []string{
			"Give me a quick overview.",
			"What are the highlights?",
			"What should I ask about?",
			"Tell me about a specific accomplishment.",
		}
	}
	if len(qs) > maxQuestions {
		qs = qs[:maxQuestions]
	}
	return qs
}

func (s Subject) FullName() string {
	_, _, full := s.NameParts()
	return full
}

// NameParts is what {{firstName}}, {{lastName}} and {{fullName}} render as.
//
// A subject with Name set is not a person: the whole name stands in for the
// first name, so copy written with {{firstName}} still reads correctly, and
// there is no last name.
func (s Subject) NameParts() (first, last, full string) {
	if name := strings.TrimSpace(s.Name); name != "" {
		return name, "", name
	}
	first, last = strings.TrimSpace(s.FirstName), strings.TrimSpace(s.LastName)
	return first, last, strings.TrimSpace(first + " " + last)
}

// PronounSet resolves the configured pronouns, defaulting by kind.
//
// An unparseable value falls back to the default here rather than returning an
// error, because validate has already rejected it at startup — carrying the
// error this far would only push a config mistake into template rendering,
// where it surfaces as a broken page instead of a clear message.
func (s Subject) PronounSet() subject.Pronouns {
	if strings.TrimSpace(s.Pronouns) == "" {
		return subject.Default(s.IsProduct())
	}
	p, err := subject.Parse(s.Pronouns)
	if err != nil {
		return subject.Default(s.IsProduct())
	}
	return p
}

// replacer expands the placeholders that appear in configured copy — the same
// list the persona uses, from the same place.
func (s Subject) replacer() *strings.Replacer {
	first, last, full := s.NameParts()
	return subject.Replacer(first, last, full, s.PronounSet())
}

// defaultDisclaimer is the shortest honest statement: it is generated, and it
// may be wrong. Anything longer is a choice the config file makes.
const defaultDisclaimer = "Powered by Generative A.I., responses may be incomplete."

// DisclaimerText resolves the small print, substituting the subject's name and
// pronouns the same way the persona does so one config serves any subject.
func (s Subject) DisclaimerText() string {
	text := strings.TrimSpace(s.Disclaimer)
	if text == "" {
		text = defaultDisclaimer
	}
	return s.replacer().Replace(text)
}

type Corpus struct {
	Path string `toml:"path"`
	// Persona is the system prompt on disk. Empty uses the built-in persona
	// for the subject's kind — see Config.PersonaPath.
	Persona string `toml:"persona"`
}

// PersonaPath is the persona file to read, falling back to the built-in copy
// if it is absent. Empty config means the kind's own file, which is also the
// file the built-in copy was compiled from.
func (c Config) PersonaPath() string {
	if p := strings.TrimSpace(c.Corpus.Persona); p != "" {
		return p
	}
	return c.Subject.PersonaFile()
}

type LLM struct {
	Vendor string `toml:"vendor"`
	Model  string `toml:"model"`
	// APIKeyRaw is the credential. It lives in the config file, so that file
	// is gitignored — see the config.example-*.toml files for the committed templates.
	APIKeyRaw string `toml:"api_key"`
	// APIKeyEnv is an optional fallback for deployments that would rather not
	// put the key on disk. Ignored when api_key is set.
	APIKeyEnv string `toml:"api_key_env"`
	BaseURL   string `toml:"base_url"`
	MaxTokens int64  `toml:"max_tokens"`
	Effort    string `toml:"effort"`
}

type Storage struct {
	// Path is the SQLite file. Empty disables persistence entirely — the chat
	// still works, nothing is recorded.
	Path string `toml:"path"`
	// RetainDays is how long questions and answers are kept. Zero, the
	// default, keeps them forever. Links and contacts are never pruned.
	RetainDays int `toml:"retain_days"`
}

// Admin guards the invite pages.
//
// Disabled unless both username and password are set — an unconfigured deploy
// serves no admin surface at all rather than an open one.
type Admin struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
	// IP optionally pins admin access to one address, checked before the
	// credentials. Correct credentials from anywhere else are refused. Accepts
	// a single address or a CIDR block, since home addresses move.
	IP string `toml:"ip"`

	// MaxAttempts is how many wrong passwords an address may try before it is
	// locked out for LockoutWindow. Zero disables it.
	//
	// Counted per client address rather than globally, so someone guessing
	// cannot lock you out of your own admin pages.
	MaxAttempts int `toml:"max_attempts"`
}

// LockoutWindow is how long an address is refused after MaxAttempts failures.
// Long enough to make guessing pointless, short enough that locking yourself
// out is an inconvenience rather than a trip to the server.
const LockoutWindow = 15 * time.Minute

// Enabled reports whether admin should be served at all.
func (a Admin) Enabled() bool {
	return a.Username != "" && a.Password != ""
}

// AllowedNet parses IP into a prefix. A bare address becomes a single-host
// prefix. Returns ok=false when no restriction is configured.
func (a Admin) AllowedNet() (netip.Prefix, bool, error) {
	return parseNet("admin.ip", a.IP)
}

// parseNet reads an address or a CIDR block. A bare address becomes a
// single-host prefix. Returns ok=false for an empty value.
func parseNet(field, value string) (netip.Prefix, bool, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return netip.Prefix{}, false, nil
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), true, nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false, fmt.Errorf("%s %q: not an address or CIDR block", field, value)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), true, nil
}

// Rates are dollars per million tokens, taken from the vendor's pricing page.
//
// Four numbers rather than one because they price very differently: a cache
// read is a fraction of uncached input, and a cache write costs more than it.
// A single blended rate is wrong in whichever direction your traffic leans.
type Rates struct {
	Input      float64 `toml:"input"`
	CacheRead  float64 `toml:"cache_read"`
	CacheWrite float64 `toml:"cache_write"`
	Output     float64 `toml:"output"`
}

// Zero reports whether nothing has been filled in, so a missing rate can fail
// loudly instead of silently costing nothing.
func (r Rates) Zero() bool {
	return r.Input == 0 && r.CacheRead == 0 && r.CacheWrite == 0 && r.Output == 0
}

// EstimateUSD prices one turn. Always an estimate — the vendor's console is
// the source of truth, and this exists to trip a guardrail, not to do
// accounting.
func (r Rates) EstimateUSD(input, cacheRead, cacheWrite, output int64) float64 {
	const perMillion = 1_000_000.0
	return (float64(input)*r.Input +
		float64(cacheRead)*r.CacheRead +
		float64(cacheWrite)*r.CacheWrite +
		float64(output)*r.Output) / perMillion
}

// Pricing maps a model id to its rates. Keyed on model alone: the same model
// costs the same whichever adapter reached it.
type Pricing map[string]Rates

func (p Pricing) For(model string) (Rates, bool) {
	r, ok := p[model]
	return r, ok
}

// Access decides who may ask questions.
//
// Only asking is gated. The page itself is always served, because an unfurler
// fetching a link carries no token and a gate at the page would leave every
// preview blank.
type Access struct {
	// InviteOnly requires a valid link before any question is answered. Off by
	// default, so a fresh deploy is an open demo rather than a locked door with
	// no way in — there are no invites until you make one.
	InviteOnly bool `toml:"invite_only"`
	// Message is shown when a link has stopped working. Deliberately vague
	// about which of expired, revoked or unknown applies: all three mean
	// someone is holding a token, and naming which one narrows a guess.
	Message string `toml:"message"`

	// NoLink is shown to someone who arrives with no link at all. A separate
	// message because it is a separate situation: they hold no token, so there
	// is nothing to give away, and this is the only thing the site says to
	// whoever finds the URL. Accepts the same {{firstName}}, {{lastName}} and
	// {{fullName}} placeholders as the disclaimer.
	NoLink string `toml:"no_link_message"`

	// MaxCostPerSession caps one visit, in dollars. Zero disables it.
	//
	// Checked against what has already been spent, so the turn that crosses the
	// line still runs — the cost of one more question is not knowable until it
	// has been asked. Set it with that overshoot in mind.
	MaxCostPerSession float64 `toml:"max_cost_per_session"`
	// MaxCostPerDay caps everyone together, in dollars, since midnight local
	// time. Zero disables it.
	//
	// This one closes the site to every visitor at once, including someone
	// mid-conversation, so it is the backstop rather than the everyday control.
	MaxCostPerDay float64 `toml:"max_cost_per_day"`

	// SessionCapMessage and DayCapMessage are shown when a cap is hit. Unlike
	// a bad link, there is nothing to hide here: the visitor did nothing wrong
	// and the honest explanation is the useful one.
	SessionCapMessage string `toml:"session_cap_message"`
	DayCapMessage     string `toml:"day_cap_message"`
}

const (
	defaultSessionCapMessage = "That is the limit for this visit. Get in touch directly to carry on."
	defaultDayCapMessage     = "This has hit its limit for today. Please try again tomorrow."
)

func (a Access) SessionCapDenied() string {
	if m := strings.TrimSpace(a.SessionCapMessage); m != "" {
		return m
	}
	return defaultSessionCapMessage
}

func (a Access) DayCapDenied() string {
	if m := strings.TrimSpace(a.DayCapMessage); m != "" {
		return m
	}
	return defaultDayCapMessage
}

// CapsEnabled reports whether any spend cap is set, which is what makes
// correct pricing load-bearing rather than merely informational.
func (a Access) CapsEnabled() bool {
	return a.MaxCostPerSession > 0 || a.MaxCostPerDay > 0
}

const (
	defaultAccessMessage = "This link is no longer active. Ask for a new one."
	// Deliberately plain. It is the front door, and whoever runs this should
	// write their own — which is why it is in the config file.
	defaultNoLinkMessage = "Access is by invitation. Get in touch with " +
		"{{firstName}} for a link."
	// A product cannot be got in touch with; the people behind it can.
	defaultProductNoLinkMessage = "Access is by invitation. Ask the people " +
		"at {{fullName}} for a link."
)

func (a Access) DeniedMessage() string {
	if m := strings.TrimSpace(a.Message); m != "" {
		return m
	}
	return defaultAccessMessage
}

// NoLinkMessage is what someone sees when they find the URL without a link.
// The name and pronouns are substituted the same way the disclaimer's are, so
// the copy survives being pointed at a different person.
func (a Access) NoLinkMessage(s Subject) string {
	text := strings.TrimSpace(a.NoLink)
	if text == "" {
		text = defaultNoLinkMessage
		if s.IsProduct() {
			text = defaultProductNoLinkMessage
		}
	}
	return s.replacer().Replace(text)
}

// Preview is what a link unfurls into in Slack, LinkedIn, iMessage and the
// like. Those crawlers arrive with no cookie, no token and no JavaScript, so
// everything here has to be in the HTML the server sends and reachable without
// authenticating.
type Preview struct {
	// Title and Description default to the subject's name and tagline. Set
	// them only to say something different in an unfurl than on the page.
	Title       string `toml:"title"`
	Description string `toml:"description"`
	// Image is the thumbnail. Defaults to avatar.photo when unset. It must be
	// a raster format — SVG is ignored by most unfurlers.
	Image string `toml:"image"`
}

// PreviewTitle is what the unfurl and the browser tab both show.
func (c Config) PreviewTitle() string {
	if t := strings.TrimSpace(c.Preview.Title); t != "" {
		return t
	}
	if name := c.Subject.FullName(); name != "" {
		return "Ask about " + name
	}
	return "Ask about…"
}

func (c Config) PreviewDescription() string {
	if d := strings.TrimSpace(c.Preview.Description); d != "" {
		return d
	}
	return c.Subject.Tagline
}

// PreviewImagePath returns the thumbnail on disk, falling back to the intro
// photograph. Empty when neither exists — an unfurl without an image is
// better than one pointing at a 404.
func (c Config) PreviewImagePath() string {
	if p := strings.TrimSpace(c.Preview.Image); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return c.Avatar.PhotoPath()
}

type Avatar struct {
	Enabled bool `toml:"enabled"`
	// Background names a backdrop from web/backgrounds.js. Unknown names fall
	// back to the default rather than rendering nothing, so a typo here
	// degrades to the wrong backdrop instead of a blank panel.
	Background string `toml:"background"`
	// Photo is a path on disk to a real photograph shown for a moment on load,
	// which then flickers and dissolves into the synthetic avatar. Optional:
	// an unset or missing file simply skips the intro.
	Photo string `toml:"photo"`
}

// PhotoPath returns the configured photo path if it exists on disk, and ""
// otherwise. A missing file is not an error — the intro is decoration.
func (a Avatar) PhotoPath() string {
	if a.Photo == "" {
		return ""
	}
	info, err := os.Stat(a.Photo)
	if err != nil || info.IsDir() {
		return ""
	}
	return a.Photo
}

// Default returns the configuration used when no file is supplied.
func Default() Config {
	return Config{
		Server: Server{Addr: ":8080"},
		Corpus: Corpus{Path: "docs/content.md"},
		LLM: LLM{
			Vendor:    "anthropic",
			Model:     "claude-opus-5",
			MaxTokens: 1024,
			Effort:    "low",
		},
		Avatar: Avatar{Background: "maxheadroom"},
		// Three wrong passwords is generous for a typo and useless for
		// guessing. Set max_attempts = 0 in the file to disable.
		Admin: Admin{MaxAttempts: 3},
		Subject: Subject{
			// The fictional subject of the corpus embedded in the binary. Only
			// ever reached when there is no config file at all.
			FirstName: "Daniel",
			LastName:  "Reyes",
			Pronouns:  "he/him",
			Tagline: "Engineering & technology executive. " +
				"Ask about roles, scope, or ways of working.",
		},
	}
}

// Load reads path on top of Default. A missing file is not an error — the
// defaults stand, which is what lets the binary run with nothing beside it.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	// Decode rather than Unmarshal, for the metadata: it reports keys the file
	// contains that nothing here reads. A typo is otherwise silent — the
	// setting simply never takes effect, and the only symptom is a feature
	// that looks broken for no visible reason.
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	// The default subject exists so a run with no config file is coherent
	// with the corpus compiled in. A file that has its own [subject] must
	// not inherit half of it: setting only firstName would otherwise produce
	// "Dana Reyes". Names the file leaves out are cleared, and validate then
	// insists on at least one.
	if meta.IsDefined("subject") {
		for key, field := range map[string]*string{
			"name": &cfg.Subject.Name, "firstName": &cfg.Subject.FirstName, "lastName": &cfg.Subject.LastName,
		} {
			if !meta.IsDefined("subject", key) {
				*field = ""
			}
		}
	}
	cfg.Unknown = undecodedKeys(meta)
	return cfg, cfg.validate()
}

// undecodedKeys lists settings present in the file that no field claims.
func undecodedKeys(meta toml.MetaData) []string {
	var out []string
	for _, key := range meta.Undecoded() {
		out = append(out, key.String())
	}
	sort.Strings(out)
	return out
}

func (c Config) validate() error {
	switch c.LLM.Vendor {
	case "anthropic", "openai", "bedrock":
	default:
		return fmt.Errorf("llm.vendor %q: want anthropic, openai, or bedrock", c.LLM.Vendor)
	}
	if c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	if c.LLM.Vendor == "bedrock" && c.LLM.BaseURL == "" {
		return fmt.Errorf("llm.base_url is required for vendor bedrock (the regional runtime host)")
	}
	switch c.Subject.Kind {
	case "", KindPerson, KindProduct, KindCompany:
	default:
		return fmt.Errorf("subject.kind %q: want person, product, or company", c.Subject.Kind)
	}
	if c.Subject.FullName() == "" {
		return fmt.Errorf("subject has no name: set firstName/lastName, or name for a product")
	}
	// The person persona addresses the subject by first name throughout, so a
	// surname alone would render "I'm sure  would be happy to discuss it".
	if first, _, _ := c.Subject.NameParts(); !c.Subject.IsProduct() && first == "" {
		return fmt.Errorf("subject.firstName is required for a person (a mononym goes in firstName)")
	}
	// Checked here so a typo is a startup error rather than a persona that
	// quietly refers to the subject as "".
	if _, err := subject.Parse(c.Subject.Pronouns); err != nil {
		return err
	}
	// Checked at load so a typo here fails at startup rather than silently
	// refusing every admin request later.
	if _, _, err := c.Admin.AllowedNet(); err != nil {
		return err
	}
	if _, _, err := c.Server.TrustedNet(); err != nil {
		return err
	}
	if c.Storage.RetainDays < 0 {
		return fmt.Errorf("storage.retain_days %d: want 0 for forever, or a number of days", c.Storage.RetainDays)
	}
	if c.Admin.IP != "" && !c.Admin.Enabled() {
		return fmt.Errorf("admin.ip is set but admin.username/admin.password are not: admin is disabled")
	}
	return nil
}

// CheckDeployable reports the combinations that cannot work, so they fail at
// startup rather than at the first visitor.
//
// Separate from validate() because these are about settings agreeing with each
// other rather than any one being malformed — and because the example config
// is tested against this, which is what catches a template that has drifted
// into something that will not start.
func (c Config) CheckDeployable() error {
	hasStore := c.Storage.Path != ""

	if c.Admin.Enabled() && !hasStore {
		return fmt.Errorf("admin.username/admin.password are set but storage.path is empty: " +
			"the admin pages manage invites, which are stored in the database, " +
			"so there would be nothing to show and no way to create one")
	}
	if c.Access.CapsEnabled() && !hasStore {
		return fmt.Errorf("access.max_cost_* requires storage.path: spend is totalled " +
			"from recorded turns, so with no database nothing would ever be counted")
	}
	if c.Access.InviteOnly && !hasStore {
		return fmt.Errorf("access.invite_only requires storage.path: with no database " +
			"there are no invites to check, so every visitor would be refused")
	}

	if c.Access.CapsEnabled() {
		rates, ok := c.Pricing.For(c.LLM.Model)
		if !ok || rates.Zero() {
			return fmt.Errorf("access.max_cost_* is set but [pricing.%q] has no rates: "+
				"every turn would price at zero and the cap would never trip. "+
				"Fill it in from the vendor's pricing page", c.LLM.Model)
		}
	}
	return nil
}

// APIKey resolves the credential: llm.api_key from the config file, falling
// back to the environment variable named by llm.api_key_env if one is set.
func (l LLM) APIKey() (string, error) {
	if key := strings.TrimSpace(l.APIKeyRaw); key != "" {
		return key, nil
	}
	if l.APIKeyEnv != "" {
		if key := os.Getenv(l.APIKeyEnv); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("llm.api_key is empty and %s is not set", l.APIKeyEnv)
	}
	return "", fmt.Errorf("llm.api_key is not set in the config file")
}
