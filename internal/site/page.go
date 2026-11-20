package site

import (
	"bytes"
	"crypto/sha256"
	_ "embed" // the explorer's script and stylesheet
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"strings"

	"iormesh/internal/viewmodel"
)

// The page is one self-contained HTML file: the stylesheet, the script
// and the model all inline, so nothing is fetched — from file:// a
// browser's notion of 'self' differs (Chrome treats file: as an opaque
// origin), and a page that loads nothing needs no 'self' at all.
//
// The CSP is default-src 'none' plus exactly two hashes: the inline
// <script> and the inline <style>. No 'unsafe-inline', no remote origin,
// no connect/img/font/frame source — a model full of production
// metadata cannot be leaked by the page, and an injected element could
// not run or load anything even if one got in. The model itself sits in
// a <script type="application/json"> block: an inert data block the
// browser never executes (so the CSP need not allow it), read by the
// script with textContent and JSON.parse.

//go:embed assets/app.js
var appJS string

//go:embed assets/site.css
var siteCSS string

// csp is the page's Content-Security-Policy; %s are the script's and
// the stylesheet's hashes.
const csp = "default-src 'none'; script-src '%s'; style-src '%s'; img-src 'none'; " +
	"connect-src 'none'; font-src 'none'; frame-src 'none'; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'"

// Page renders the explorer's index.html for m. Same model, same bytes:
// the assets are fixed, encoding/json writes struct fields in order, and
// nothing of the build machine or the clock goes in.
func Page(m viewmodel.Model) ([]byte, error) {
	data, err := modelJSON(m)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	fmt.Fprintf(&b, "<meta http-equiv=\"Content-Security-Policy\" content=\"%s\">\n",
		fmt.Sprintf(csp, hash(appJS), hash(siteCSS)))
	b.WriteString("<meta name=\"referrer\" content=\"no-referrer\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>iormesh " + html.EscapeString(strings.Join(m.Run.Projects, "+")) + "</title>\n")
	b.WriteString("<style>" + siteCSS + "</style>\n</head>\n<body>\n")
	b.WriteString("<header><h1 id=\"title\"></h1><p id=\"run\"></p></header>\n" +
		"<nav id=\"tabs\"></nav>\n<div id=\"filters\"></div>\n" +
		"<main><section id=\"view\"><noscript>The explorer needs JavaScript; " +
		"model.json and iormeshctl show have the same data.</noscript></section>" +
		"<aside id=\"detail\"></aside></main>\n")
	b.WriteString("<script type=\"application/json\" id=\"model\">")
	b.Write(data)
	b.WriteString("</script>\n<script>" + appJS + "</script>\n</body>\n</html>\n")
	return b.Bytes(), nil
}

// modelJSON is the model as JSON that cannot leave its script element.
// encoding/json writes '<', '>', '&', U+2028 and U+2029 as six-character
// backslash-u escapes, so no "</script", "<!--" or line
// terminator reaches the HTML parser; invalid UTF-8 becomes U+FFFD. The
// escaping is asked for explicitly (SetEscapeHTML) rather than relied on
// as a default, and checked once more: the page must never be built
// around a block that could close early.
func modelJSON(m viewmodel.Model) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(m); err != nil {
		return nil, fmt.Errorf("site: model: %w", err)
	}
	out := bytes.TrimRight(b.Bytes(), "\n")
	if bytes.ContainsAny(out, "<>&") || bytes.Contains(out, []byte("\u2028")) ||
		bytes.Contains(out, []byte("\u2029")) {
		return nil, fmt.Errorf("site: model JSON holds an unescaped HTML character")
	}
	return out, nil
}

// hash is a CSP source expression for an inline element's exact text.
func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}
