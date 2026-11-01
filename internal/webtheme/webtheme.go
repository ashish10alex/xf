// Package webtheme is xf web's colour themes: each one's name, whether it
// is light or dark, and its colours, from which xf web's themes.css is
// built. A theme colours every token the page's stylesheet reads, so it
// recolours the whole page, not only its code.
package webtheme

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Kinds of theme: auto shows a light one while the browser's colour
// scheme is light and a dark one while it is dark.
const (
	Light = "light"
	Dark  = "dark"
)

// Auto is the webTheme that follows the browser's colour scheme, showing
// the webThemeLight or the webThemeDark theme; DefaultLight and
// DefaultDark are those when nothing sets them: xf's own.
const (
	Auto         = "auto"
	DefaultLight = "xf-light"
	DefaultDark  = "xf-dark"
)

// Theme is one colour theme.
type Theme struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // Light or Dark
	// Colors are #rrggbb colours.
	Colors Colors `json:"-"`
}

// Colors are a theme's colours, each the CSS custom property its comment
// names. Every one must be set.
type Colors struct {
	Bg     string // --bg: what is around the panes
	Bg2    string // --bg-2: a row under the mouse, a box set off its pane
	Panel  string // --panel: the panes
	Fg     string // --fg: text
	Dim    string // --dim: text that steps back, and the gutters
	Rule   string // --rule: the hairline boxes
	Focus  string // --focus: the focused pane and the cursor, a green
	Sel    string // --sel: the selected row
	Visual string // --visual: selected text
	Hit    string // --hit: search matches
	Amber  string // --amber: what is under way and warnings, a yellow
	Red    string // --red
	Green  string // --green
	Blue   string // --blue
	Purple string // --purple
	Pink   string // --pink

	// The code's tokens.
	Keyword  string // --t-kw
	Function string // --t-fn: a function called or defined
	Builtin  string // --t-builtin: a function SQL has, as COUNT
	Type     string // --t-type
	String   string // --t-str
	Number   string // --t-num
	Constant string // --t-const: true, false, null
	Comment  string // --t-com
	Operator string // --t-op
	Name     string // --t-name: a table or a column
	Variable string // --t-var: @a_variable
	Property string // --t-prop: a field of an object, as in a config block
}

// tokens are c's colours by custom property, in the stylesheet's order.
func (c Colors) tokens() [][2]string {
	return [][2]string{
		{"bg", c.Bg}, {"bg-2", c.Bg2}, {"panel", c.Panel}, {"fg", c.Fg}, {"dim", c.Dim}, {"rule", c.Rule},
		{"focus", c.Focus}, {"sel", c.Sel}, {"visual", c.Visual}, {"hit", c.Hit},
		{"amber", c.Amber}, {"red", c.Red}, {"green", c.Green}, {"blue", c.Blue}, {"purple", c.Purple}, {"pink", c.Pink},
		{"t-kw", c.Keyword}, {"t-fn", c.Function}, {"t-builtin", c.Builtin}, {"t-type", c.Type}, {"t-str", c.String},
		{"t-num", c.Number}, {"t-const", c.Constant}, {"t-com", c.Comment}, {"t-op", c.Operator}, {"t-name", c.Name},
		{"t-var", c.Variable}, {"t-prop", c.Property},
	}
}

// All is every theme, in the order the picker lists them: xf's own, then
// the rest by name.
func All() []Theme { return themes }

// Find is the theme id names. light and dark, the values webTheme had
// before there were themes, name xf's own.
func Find(id string) (Theme, bool) {
	switch id {
	case Light:
		id = DefaultLight
	case Dark:
		id = DefaultDark
	}
	for _, t := range themes {
		if t.ID == id {
			return t, true
		}
	}
	return Theme{}, false
}

// IDs are the themes' ids, as a message lists them.
func IDs() string {
	ids := make([]string, len(themes))
	for i, t := range themes {
		ids[i] = t.ID
	}
	return strings.Join(ids, ", ")
}

// Choice is what the page shows: Theme, auto or a theme's id, and the
// themes auto shows in a light and in a dark browser.
type Choice struct {
	Theme string `json:"theme"`
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

// Resolved is c with its values checked: one that names no theme is the
// default, and light and dark are xf's own themes' ids.
func (c Choice) Resolved() Choice {
	id := func(v, def string) string {
		if t, ok := Find(v); ok {
			return t.ID
		}
		return def
	}
	out := Choice{Theme: Auto, Light: id(c.Light, DefaultLight), Dark: id(c.Dark, DefaultDark)}
	if c.Theme != Auto {
		out.Theme = id(c.Theme, Auto)
	}
	return out
}

// CSS is themes.css: every theme's custom properties, set on
// [data-theme="its id"], and xf's own as the page's colours until the
// page sets a theme. The page sets a theme on <html>; an element of its
// own with data-theme, such as a swatch in the picker, takes that
// theme's colours.
var CSS = sync.OnceValue(func() string {
	var b strings.Builder
	b.WriteString("/* xf web's colour themes, built by internal/webtheme. */\n")
	light, _ := Find(DefaultLight)
	dark, _ := Find(DefaultDark)
	b.WriteString(":root {\n")
	writeTokens(&b, light, "  ")
	b.WriteString("}\n@media (prefers-color-scheme: dark) {\n  :root:not([data-theme]) {\n")
	writeTokens(&b, dark, "    ")
	b.WriteString("  }\n}\n")
	for _, t := range themes {
		fmt.Fprintf(&b, "[data-theme=%q] {\n", t.ID)
		writeTokens(&b, t, "  ")
		b.WriteString("}\n")
	}
	return b.String()
})

// writeTokens writes t's custom properties, and those that follow from
// its kind and its colours.
func writeTokens(b *strings.Builder, t Theme, indent string) {
	fmt.Fprintf(b, "%scolor-scheme: %s;\n", indent, t.Kind)
	for _, kv := range t.Colors.tokens() {
		fmt.Fprintf(b, "%s--%s: %s;\n", indent, kv[0], kv[1])
	}
	glow, badge, shadow, grain := 0.14, "#ffffff", "0 18px 60px rgba(40, 32, 12, 0.18), 0 2px 8px rgba(40, 32, 12, 0.08)", "0.035"
	if t.Kind == Dark {
		glow, badge, shadow, grain = 0.10, t.Colors.Bg, "0 24px 80px rgba(0, 0, 0, 0.6)", "0.05"
	}
	r, g, bl := rgb(t.Colors.Focus)
	fmt.Fprintf(b, "%s--focus-glow: rgba(%d, %d, %d, %.2f);\n", indent, r, g, bl, glow)
	fmt.Fprintf(b, "%s--badge-fg: %s;\n%s--shadow: %s;\n%s--grain: %s;\n", indent, badge, indent, shadow, indent, grain)
}

// IsHex reports whether c is a #rrggbb colour.
func IsHex(c string) bool {
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(c[1:], 16, 32)
	return err == nil
}

// rgb is #rrggbb's channels; a colour that isn't one is black.
func rgb(c string) (r, g, b int) {
	if !IsHex(c) {
		return 0, 0, 0
	}
	n, _ := strconv.ParseUint(c[1:], 16, 32)
	return int(n >> 16 & 0xff), int(n >> 8 & 0xff), int(n & 0xff)
}

// mix is a laid over b at opacity t (0 to 1), as one #rrggbb colour: how
// a theme's translucent highlight looks on its panes.
func mix(a, b string, t float64) string {
	ar, ag, ab := rgb(a)
	br, bg, bb := rgb(b)
	ch := func(x, y int) int { return int(float64(x)*t + float64(y)*(1-t) + 0.5) }
	return fmt.Sprintf("#%02x%02x%02x", ch(ar, br), ch(ag, bg), ch(ab, bb))
}
