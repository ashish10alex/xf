package webtheme

import (
	"math"
	"strings"
	"testing"
)

func TestEveryThemeHasEveryColour(t *testing.T) {
	seen := map[string]bool{}
	for i, th := range All() {
		if th.ID == "" || th.Name == "" || seen[th.ID] {
			t.Errorf("theme %d: id %q, name %q (a second one?)", i, th.ID, th.Name)
		}
		seen[th.ID] = true
		if th.Kind != Light && th.Kind != Dark {
			t.Errorf("%s: kind %q", th.ID, th.Kind)
		}
		for _, kv := range th.Colors.tokens() {
			if !IsHex(kv[1]) {
				t.Errorf("%s: --%s is %q, not #rrggbb", th.ID, kv[0], kv[1])
			}
		}
	}
	for _, id := range []string{DefaultLight, DefaultDark} {
		if !seen[id] {
			t.Errorf("no %s", id)
		}
	}
}

// The picker lists xf's own themes first, then the rest by name.
func TestThemesAreInOrder(t *testing.T) {
	all := All()
	if all[0].ID != DefaultLight || all[1].ID != DefaultDark {
		t.Fatalf("first %s, %s", all[0].ID, all[1].ID)
	}
	for i := 3; i < len(all); i++ {
		if strings.ToLower(all[i-1].Name) > strings.ToLower(all[i].Name) {
			t.Errorf("%s before %s", all[i-1].Name, all[i].Name)
		}
	}
}

// Text must read on a theme's panes and what is around them; the colours
// that step back are left as the theme has them.
func TestTextContrast(t *testing.T) {
	for _, th := range All() {
		c := th.Colors
		for _, bg := range []string{c.Bg, c.Panel} {
			if r := contrast(c.Fg, bg); r < 4.5 {
				t.Errorf("%s: --fg %s on %s is %.2f:1, under 4.5:1", th.ID, c.Fg, bg, r)
			}
		}
	}
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func luminance(c string) float64 {
	r, g, b := rgb(c)
	lin := func(v int) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

func TestFind(t *testing.T) {
	for id, want := range map[string]string{"dracula": "dracula", "light": DefaultLight, "dark": DefaultDark, "xf-dark": DefaultDark} {
		if th, ok := Find(id); !ok || th.ID != want {
			t.Errorf("Find(%q) = %s, %v", id, th.ID, ok)
		}
	}
	for _, id := range []string{"", "auto", "sepia"} {
		if _, ok := Find(id); ok {
			t.Errorf("Find(%q) found one", id)
		}
	}
}

func TestResolved(t *testing.T) {
	cases := []struct{ in, want Choice }{
		{Choice{}, Choice{Auto, DefaultLight, DefaultDark}},
		{Choice{Theme: "dark"}, Choice{DefaultDark, DefaultLight, DefaultDark}},
		{Choice{Theme: "sepia", Light: "catppuccin-latte", Dark: "nope"}, Choice{Auto, "catppuccin-latte", DefaultDark}},
		{Choice{Theme: "auto", Light: "light", Dark: "nord"}, Choice{Auto, DefaultLight, "nord"}},
	}
	for _, c := range cases {
		if got := c.in.Resolved(); got != c.want {
			t.Errorf("%+v resolved %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestCSS(t *testing.T) {
	css := CSS()
	for _, th := range All() {
		if !strings.Contains(css, `[data-theme="`+th.ID+`"] {`) {
			t.Errorf("no block for %s", th.ID)
		}
	}
	for _, want := range []string{"--t-builtin: #", "--focus-glow: rgba(", "color-scheme: dark;", "@media (prefers-color-scheme: dark)"} {
		if !strings.Contains(css, want) {
			t.Errorf("themes.css has no %q", want)
		}
	}
}

func TestMix(t *testing.T) {
	if got := mix("#ffffff", "#000000", 0.5); got != "#808080" {
		t.Errorf("mix = %s", got)
	}
	if got := mix("#ff0000", "#0000ff", 1); got != "#ff0000" {
		t.Errorf("mix = %s", got)
	}
}
