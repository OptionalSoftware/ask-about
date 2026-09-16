package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// examplePaths are the committed templates, one per kind of subject, two
// directories up.
var examplePaths = []string{
	"../config.example-person.toml",
	"../config.example-product.toml",
	"../config.example-company.toml",
}

// eachExample runs f against every committed template.
func eachExample(t *testing.T, f func(t *testing.T, path, text string)) {
	t.Helper()
	for _, path := range examplePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Base(path), err)
		}
		t.Run(filepath.Base(path), func(t *testing.T) { f(t, path, string(data)) })
	}
}

// TestExampleCoversEverySetting keeps the examples from drifting behind the
// struct.
//
// The example is the only place a setting is described, so one added here and
// not there is invisible: you would have to read the source to know it exists.
// That is how a live config ends up missing whole sections, and the symptom is
// a feature that looks broken rather than one that looks unconfigured.
func TestExampleCoversEverySetting(t *testing.T) {
	eachExample(t, func(t *testing.T, path, text string) {
		for _, key := range tomlKeys(reflect.TypeOf(Config{})) {
			// Matches the key at the start of a line, set or commented out —
			// documenting it as a commented example still counts as covering it.
			re := regexp.MustCompile(`(?m)^\s*#?\s*` + regexp.QuoteMeta(key) + `\s*=`)
			if !re.MatchString(text) {
				t.Errorf("%s is not in %s", key, filepath.Base(path))
			}
		}
	})
}

// TestExampleHasEverySection is the coarser check: a whole missing section is
// what actually happened, and it is easier to read in a failure than a list of
// individual keys.
func TestExampleHasEverySection(t *testing.T) {
	eachExample(t, func(t *testing.T, path, text string) {
		cfgType := reflect.TypeOf(Config{})
		for i := range cfgType.NumField() {
			tag := tomlName(cfgType.Field(i))
			if tag == "" {
				continue
			}
			// Pricing is a map keyed by model, so it appears as [pricing."model"].
			if !strings.Contains(text, "["+tag+"]") && !strings.Contains(text, "["+tag+".") {
				t.Errorf("[%s] is not in %s", tag, filepath.Base(path))
			}
		}
	})
}

// TestExampleLoads is the end of the same argument: the template has to be a
// working config, not just a complete one.
//
// CheckDeployable is the part that matters. Parsing was never the problem —
// the example shipped for a while with a model that had no [pricing] block and
// spend caps switched on, which parses perfectly and then refuses to start.
func TestExampleLoads(t *testing.T) {
	eachExample(t, func(t *testing.T, path, _ string) {
		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("the committed example does not load: %v", err)
		}
		if err := cfg.CheckDeployable(); err != nil {
			t.Fatalf("copying the example and running it would fail: %v", err)
		}
		// The pricing block has to name the model actually configured.
		if rates, ok := cfg.Pricing.For(cfg.LLM.Model); !ok || rates.Zero() {
			t.Errorf("[pricing.%q] is missing or all zeroes", cfg.LLM.Model)
		}
		if len(cfg.Unknown) != 0 {
			t.Errorf("the example sets keys nothing reads: %v", cfg.Unknown)
		}
		// Defaults have to survive an example that leaves things blank.
		if cfg.Subject.DisclaimerText() == "" {
			t.Error("disclaimer resolved to empty")
		}
		if cfg.PreviewTitle() == "" {
			t.Error("preview title resolved to empty")
		}
		// The corpus each example points at has to exist, or the first run
		// answers as the built-in sample with only a log line to say so.
		if _, err := os.Stat(filepath.Join("..", cfg.Corpus.Path)); err != nil &&
			!strings.HasPrefix(cfg.Corpus.Path, "private/") {
			t.Errorf("corpus.path %q does not exist in the checkout", cfg.Corpus.Path)
		}
		// Each example is what it says it is.
		wantProduct := !strings.HasSuffix(path, "person.toml")
		if cfg.Subject.IsProduct() != wantProduct {
			t.Errorf("kind = %q", cfg.Subject.Kind)
		}
	})
}

// The three examples differ only in part 1. Parts 2 and 3 — where it runs,
// who may use it, the model and its cost — are byte-identical, so a fix to
// one cannot quietly miss the others.
func TestExamplesShareTheirLaterParts(t *testing.T) {
	const marker = "# 2. WHERE IT RUNS"
	var first string
	eachExample(t, func(t *testing.T, path, text string) {
		i := strings.Index(text, marker)
		if i < 0 {
			t.Fatalf("%s has no part 2 banner", filepath.Base(path))
		}
		tail := text[i:]
		if first == "" {
			first = tail
			return
		}
		if tail != first {
			t.Errorf("parts 2 and 3 of %s differ from the person example", filepath.Base(path))
		}
	})
}

// TestUnknownKeysAreReported covers the other half: a typo in the user's own
// file parses cleanly and then does nothing at all.
func TestUnknownKeysAreReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typo.toml")
	err := os.WriteFile(path, []byte(`
[llm]
vendor = "anthropic"
model  = "claude-sonnet-5"

[preview]
imgae = "docs/photo.jpg"

[avatar]
enable = true
`), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := strings.Join(cfg.Unknown, ",")
	for _, want := range []string{"preview.imgae", "avatar.enable"} {
		if !strings.Contains(got, want) {
			t.Errorf("unknown keys = %v, want it to include %s", cfg.Unknown, want)
		}
	}
	// And nothing correctly spelled should be reported.
	if strings.Contains(got, "llm.vendor") {
		t.Errorf("a valid key was reported as unknown: %v", cfg.Unknown)
	}
}

// tomlName returns a field's toml key, or "" when it has none or is skipped.
func tomlName(f reflect.StructField) string {
	tag := f.Tag.Get("toml")
	if tag == "" || tag == "-" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// tomlKeys walks a struct and returns every leaf toml key.
func tomlKeys(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		name := tomlName(f)
		if name == "" {
			continue
		}
		if f.Type.Kind() == reflect.Struct {
			out = append(out, tomlKeys(f.Type)...)
			continue
		}
		if f.Type.Kind() == reflect.Map {
			// A map's keys come from the file, not the struct; the section
			// check covers it.
			continue
		}
		out = append(out, name)
	}
	return out
}
