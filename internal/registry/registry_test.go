package registry

import (
	"reflect"
	"testing"

	"astudio2api/internal/astron"
)

func fixture() []*Model {
	return []*Model{
		{Slug: "xopglm52", Name: "GLM-5.2", DirectoryID: "lm_glm52", Priority: 1},
		{Slug: "xdeepseek", Name: "DeepSeek-V3", DirectoryID: "lm_ds", Priority: 2},
		{Slug: "xqwen", Name: "Qwen3", DirectoryID: "lm_qwen", Priority: 2},
	}
}

func newFixture() *Registry {
	r := New()
	r.Replace(fixture(), "test", "")
	return r
}

func TestResolveAndSlugFor(t *testing.T) {
	r := newFixture()
	tests := []struct {
		name     string
		in       string
		resolved string // "" means Resolve must return nil (unknown name)
		slug     string // SlugFor passes unknown names through unchanged
	}{
		{name: "slug", in: "xopglm52", resolved: "xopglm52", slug: "xopglm52"},
		{name: "display name", in: "GLM-5.2", resolved: "xopglm52", slug: "xopglm52"},
		{name: "directory id", in: "lm_glm52", resolved: "xopglm52", slug: "xopglm52"},
		{name: "case insensitive", in: "gLm-5.2", resolved: "xopglm52", slug: "xopglm52"},
		{name: "surrounding whitespace", in: "  xdeepseek  ", resolved: "xdeepseek", slug: "xdeepseek"},
		{name: "unknown passes through", in: "some-new-model", resolved: "", slug: "some-new-model"},
		{name: "empty passes through", in: "", resolved: "", slug: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Resolve(tc.in)
			if tc.resolved == "" {
				if got != nil {
					t.Fatalf("Resolve(%q) = %q, want nil", tc.in, got.Slug)
				}
			} else if got == nil {
				t.Fatalf("Resolve(%q) = nil, want slug %q", tc.in, tc.resolved)
			} else if got.Slug != tc.resolved {
				t.Fatalf("Resolve(%q).Slug = %q, want %q", tc.in, got.Slug, tc.resolved)
			}
			if gotSlug := r.SlugFor(tc.in); gotSlug != tc.slug {
				t.Fatalf("SlugFor(%q) = %q, want %q", tc.in, gotSlug, tc.slug)
			}
		})
	}
}

func TestAliasOverride(t *testing.T) {
	r := newFixture()
	r.SetAliases(map[string]string{"glm": "xopglm52", "fast": "GLM-5.2"})
	for _, in := range []string{"glm", "GLM", "fast"} {
		if got := r.SlugFor(in); got != "xopglm52" {
			t.Fatalf("SlugFor(%q) = %q, want xopglm52", in, got)
		}
	}
	// An alias pointing at an unknown target must be ignored, not resolve to nil
	// from a lookup that should fall back to the raw name.
	r.SetAliases(map[string]string{"ghost": "does-not-exist"})
	if got := r.SlugFor("ghost"); got != "ghost" {
		t.Fatalf("SlugFor(ghost) = %q, want passthrough", got)
	}
}

func TestListOrdering(t *testing.T) {
	r := newFixture()
	got := r.List()
	want := []string{"xopglm52", "xdeepseek", "xqwen"} // priority, then name
	var slugs []string
	for _, m := range got {
		slugs = append(slugs, m.Slug)
	}
	if !reflect.DeepEqual(slugs, want) {
		t.Fatalf("List() = %v, want %v", slugs, want)
	}
	// List must not alias the internal slice.
	got[0] = &Model{Slug: "mutated"}
	if r.List()[0].Slug != "xopglm52" {
		t.Fatal("List() returned a slice that aliases internal state")
	}
}

func TestStatus(t *testing.T) {
	r := newFixture()
	count, source, refreshedAt, lastErr := r.Status()
	if count != 3 || source != "test" || lastErr != "" || refreshedAt.IsZero() {
		t.Fatalf("Status() = (%d, %q, %v, %q)", count, source, refreshedAt, lastErr)
	}
}

func TestFromCatalog(t *testing.T) {
	c := astron.CatalogModel{
		Slug:             "xopglm52",
		DisplayName:      "GLM-5.2",
		Description:      "desc",
		ContextWindow:    200000,
		Priority:         3,
		SupportedInAPI:   true,
		InputModalities:  []string{"text", "image"},
		DefaultReasoning: "medium",
		ReasoningLevels: []astron.ReasoningSpec{
			{Effort: "low"},
			{Effort: ""}, // must be dropped
			{Effort: "high"},
		},
	}
	m := fromCatalog(c)
	if m.Slug != "xopglm52" || m.Name != "GLM-5.2" || m.ContextWindow != 200000 {
		t.Fatalf("fromCatalog mapped identity fields wrong: %+v", m)
	}
	if m.Priority != 3 || !m.Available || m.Source != "model-manager" {
		t.Fatalf("fromCatalog mapped meta fields wrong: %+v", m)
	}
	if want := []string{"low", "high"}; !reflect.DeepEqual(m.ReasoningEfforts, want) {
		t.Fatalf("ReasoningEfforts = %v, want %v", m.ReasoningEfforts, want)
	}

	// A catalog entry without a display name falls back to the slug.
	if got := fromCatalog(astron.CatalogModel{Slug: "xplain"}); got.Name != "xplain" {
		t.Fatalf("name fallback = %q, want xplain", got.Name)
	}
}

func TestFromConfig(t *testing.T) {
	tests := []struct {
		name string
		in   astron.ModelConfig
		want Model
	}{
		{
			name: "full entry",
			in: astron.ModelConfig{
				ID: "lm_glm52", Name: "GLM-5.2", Model: "xopglm52",
				Provider: "zhipu", PointMultiplier: "0.5",
			},
			want: Model{
				Slug: "xopglm52", Name: "GLM-5.2", DirectoryID: "lm_glm52",
				Provider: "zhipu", PointMultiplier: "0.5",
				Available: true, Source: "bot/models/configs",
			},
		},
		{
			name: "slug falls back to id",
			in:   astron.ModelConfig{ID: "lm_x", Name: "X"},
			want: Model{
				Slug: "lm_x", Name: "X", DirectoryID: "lm_x",
				Available: true, Source: "bot/models/configs",
			},
		},
		{
			name: "name falls back to slug",
			in:   astron.ModelConfig{Model: "xqwen"},
			want: Model{
				Slug: "xqwen", Name: "xqwen",
				Available: true, Source: "bot/models/configs",
			},
		},
		{
			name: "numeric point multiplier",
			in:   astron.ModelConfig{Model: "xn", Name: "N", PointMultiplier: float64(2.5)},
			want: Model{
				Slug: "xn", Name: "N", PointMultiplier: "2.5",
				Available: true, Source: "bot/models/configs",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fromConfig(tc.in)
			if !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("fromConfig() = %+v, want %+v", *got, tc.want)
			}
		})
	}
}

func TestSyncError(t *testing.T) {
	if got := (&SyncError{}).Error(); got != "model directory refresh produced no models" {
		t.Fatalf("empty SyncError = %q", got)
	}
	if got := (&SyncError{Messages: []string{"a", "b"}}).Error(); got != "model directory refresh failed: a; b" {
		t.Fatalf("SyncError = %q", got)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"  GLM-5.2 ": "glm-5.2", "": "", "\tX\t": "x"} {
		if got := normalize(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
