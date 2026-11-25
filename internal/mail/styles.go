package mail

import (
	"regexp"
	"strings"

	"github.com/aymerick/douceur/css"
	"github.com/aymerick/douceur/parser"
	"golang.org/x/net/html"
)

// Preserve common email layouts and Unicode font names. Resource loads, CSS
// escapes, custom properties and fixed positioning remain blocked.
const mailStyleProperties = "color background background-color font font-family font-size font-weight font-style font-variant line-height letter-spacing word-spacing text-align text-decoration text-transform text-indent white-space word-break overflow-wrap vertical-align display width min-width max-width height min-height max-height margin margin-top margin-right margin-bottom margin-left padding padding-top padding-right padding-bottom padding-left border border-top border-right border-bottom border-left border-color border-width border-style border-radius border-collapse border-spacing table-layout list-style-type list-style-position float clear box-sizing font-stretch font-variant-numeric text-decoration-color text-decoration-style text-decoration-thickness text-underline-offset text-overflow text-shadow word-wrap hyphens overflow overflow-x overflow-y opacity box-shadow outline outline-color outline-style outline-width outline-offset border-top-color border-right-color border-bottom-color border-left-color border-top-width border-right-width border-bottom-width border-left-width border-top-style border-right-style border-bottom-style border-left-style border-top-left-radius border-top-right-radius border-bottom-left-radius border-bottom-right-radius background-size background-position background-repeat background-origin background-clip flex flex-basis flex-grow flex-shrink flex-direction flex-wrap flex-flow order justify-content justify-items justify-self align-content align-items align-self place-content place-items place-self gap row-gap column-gap grid grid-template grid-template-columns grid-template-rows grid-template-areas grid-auto-columns grid-auto-rows grid-auto-flow grid-column grid-column-start grid-column-end grid-row grid-row-start grid-row-end grid-area position top right bottom left inset z-index transform transform-origin object-fit object-position aspect-ratio visibility direction unicode-bidi"

var safeCSSValue = regexp.MustCompile(`^[\p{L}\p{N}\s#.,%()+/'"!:_\[\]-]+$`)

func safeStyleValue(v string) bool {
	lower := strings.ToLower(v)
	return safeCSSValue.MatchString(v) && !strings.ContainsAny(v, "\\<>@") && !strings.Contains(lower, "url") && !strings.Contains(lower, "expression") && !strings.Contains(lower, "image") && !strings.Contains(lower, "var(") && !strings.Contains(lower, "attr(")
}
func safePosition(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "static", "relative", "absolute", "sticky", "initial", "inherit", "unset", "revert":
		return true
	}
	return false
}

func cleanDeclarations(ds []*css.Declaration) []*css.Declaration {
	out := []*css.Declaration{}
	for _, d := range ds {
		if strings.Contains(" "+mailStyleProperties+" ", " "+strings.ToLower(d.Property)+" ") && safeStyleValue(d.Value) && (strings.ToLower(d.Property) != "position" || safePosition(d.Value)) {
			out = append(out, d)
		}
	}
	return out
}
func cleanRules(rs []*css.Rule, depth int) []*css.Rule {
	if depth > 8 {
		return nil
	}
	out := []*css.Rule{}
	for _, r := range rs {
		if r.Kind == css.QualifiedRule {
			if strings.ContainsAny(r.Prelude, "\\<>@") {
				continue
			}
			r.Declarations = cleanDeclarations(r.Declarations)
			if len(r.Declarations) > 0 {
				out = append(out, r)
			}
		} else if (strings.EqualFold(r.Name, "@media") || strings.EqualFold(r.Name, "@supports")) && safeStyleValue(r.Prelude) {
			r.Rules = cleanRules(r.Rules, depth+1)
			if len(r.Rules) > 0 {
				out = append(out, r)
			}
		}
	}
	return out
}

// Extract style blocks before the HTML sanitizer removes them, then restore only
// parsed, allowlisted CSS. Never enable arbitrary unsafe HTML in bluemonday.
func mailStyles(source string) string {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return ""
	}
	var out strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			var raw strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					raw.WriteString(c.Data)
				}
			}
			sheet, e := parser.Parse(raw.String())
			if e == nil {
				for _, r := range cleanRules(sheet.Rules, 0) {
					out.WriteString(r.String())
				}
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if out.Len() == 0 {
		return ""
	}
	return "<style>" + out.String() + "</style>"
}
