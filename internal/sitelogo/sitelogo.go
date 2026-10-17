// Package sitelogo owns the site logo selection rules shared by the public
// manifest, OG generation and email rendering.
//
// Display logos come in two forms (full lockup and symbol), each with a light
// and a dark variant. A missing theme uses the other theme of the same form; a
// missing form uses the other form. Email logos only fall back across themes:
// they are PNG for mail clients, so an SVG display logo is never substituted.
package sitelogo

// Form is the shape of a display logo.
type Form int

const (
	Full Form = iota
	Symbol
)

// Theme is the background a logo is drawn for.
type Theme int

const (
	Light Theme = iota
	Dark
)

// Display holds the configured display logo slots; nil means unset.
type Display[T any] struct {
	FullLight, FullDark, SymbolLight, SymbolDark *T
}

// Resolve returns the logo for form and theme, falling back to the other theme
// of the same form, then to the other form (same theme first). Nil only when no
// display logo is configured.
func (d Display[T]) Resolve(form Form, theme Theme) *T {
	other := Symbol
	if form == Symbol {
		other = Full
	}
	for _, candidate := range []*T{
		d.slot(form, theme), d.slot(form, opposite(theme)),
		d.slot(other, theme), d.slot(other, opposite(theme)),
	} {
		if candidate != nil {
			return candidate
		}
	}
	return nil
}

// Resolved returns every display slot with the fallback rules applied.
func (d Display[T]) Resolved() Display[T] {
	return Display[T]{
		FullLight:   d.Resolve(Full, Light),
		FullDark:    d.Resolve(Full, Dark),
		SymbolLight: d.Resolve(Symbol, Light),
		SymbolDark:  d.Resolve(Symbol, Dark),
	}
}

func (d Display[T]) slot(form Form, theme Theme) *T {
	switch {
	case form == Full && theme == Light:
		return d.FullLight
	case form == Full:
		return d.FullDark
	case theme == Light:
		return d.SymbolLight
	default:
		return d.SymbolDark
	}
}

// Email holds the configured email logo slots; nil means unset.
type Email[T any] struct {
	Light, Dark *T
}

// Resolve returns the email logo for theme, or the other theme when unset.
func (e Email[T]) Resolve(theme Theme) *T {
	first, second := e.Light, e.Dark
	if theme == Dark {
		first, second = e.Dark, e.Light
	}
	if first != nil {
		return first
	}
	return second
}

func opposite(theme Theme) Theme {
	if theme == Light {
		return Dark
	}
	return Light
}
