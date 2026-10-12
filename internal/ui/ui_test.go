package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func TestServesTheEmbeddedPage(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type = %q, want text/html", ct)
	}
	if rp := resp.Header.Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer: outbound links must not carry the instance address", rp)
	}
	buf := make([]byte, 1024)
	n, _ := io.ReadFull(resp.Body, buf)
	if !strings.Contains(string(buf[:n]), "<title>sierpe</title>") {
		t.Error("the first KiB must carry <title>sierpe</title>")
	}
}

func TestRootPatternDoesNotShadowAPIPaths(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/contracts")
	if err != nil {
		t.Fatalf("GET /v1/contracts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 from the mux, not the UI page", resp.StatusCode)
	}
}

// The page is part of the binary: it must work offline, on a tailnet, and
// load nothing from anywhere else.
func TestPageLoadsNoExternalAssets(t *testing.T) {
	page := string(indexHTML)
	for _, banned := range []string{"<script src", "@import", "url(http", `src="http`, "src='http", "<iframe", "<object", "<embed"} {
		if strings.Contains(strings.ToLower(page), strings.ToLower(banned)) {
			t.Errorf("page contains %q: no external assets", banned)
		}
	}
	for _, link := range regexp.MustCompile(`(?i)<link\b[^>]*>`).FindAllString(page, -1) {
		if !strings.Contains(link, `rel="icon"`) || !strings.Contains(link, `href="data:`) {
			t.Errorf("only an inline data: favicon may use <link>, got %.80s", link)
		}
	}
	// Absolute URLs are either the SVG namespace or the outbound explorer
	// links; anything else is an asset or a tracker slipping in.
	allowed := []string{"http://www.w3.org/2000/svg", "https://stellar.expert/"}
	for _, u := range regexp.MustCompile(`https?://[^\s'"<>)]+`).FindAllString(page, -1) {
		ok := false
		for _, a := range allowed {
			ok = ok || strings.HasPrefix(u, a)
		}
		if !ok {
			t.Errorf("absolute URL %q is not on the allowlist %v", u, allowed)
		}
	}
}

// The docs promise nothing is written to the browser; the admin token in
// particular lives only in the page's memory.
func TestPageUsesNoBrowserStorage(t *testing.T) {
	for _, banned := range []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie"} {
		if strings.Contains(string(indexHTML), banned) {
			t.Errorf("page references %s", banned)
		}
	}
}

func TestPageSizeBudget(t *testing.T) {
	const budget = 96 << 10
	if len(indexHTML) > budget {
		t.Errorf("index.html is %d bytes, budget %d: it is served on every visit and embedded in the binary", len(indexHTML), budget)
	}
}

// Chain data is attacker-controlled (mainnet strings carry quotes and angle
// brackets), so it reaches the page only as DOM text. HTML strings are left
// in exactly one place, the register box, which shows the server's own
// classification escaped.
func TestHTMLSinksStayInTheRegisterBox(t *testing.T) {
	page := string(indexHTML)
	for _, banned := range []string{"insertAdjacentHTML", "outerHTML", "document.write", "eval(", "new Function"} {
		if strings.Contains(page, banned) {
			t.Errorf("page uses %s", banned)
		}
	}
	uses := strings.Count(page, "innerHTML")
	inRegisterBox := strings.Count(page, `$("regmsg").innerHTML = `)
	if uses != inRegisterBox || uses > 2 {
		t.Errorf("innerHTML used %d times, %d of them in the register box (want only the register box, at most 2)", uses, inRegisterBox)
	}
}

// Invisible characters in the page's own source (bidi overrides, zero-width
// marks) would make the code read differently from what it runs; the page
// spells them as \u escapes instead.
func TestPageSourceHasNoInvisibleCharacters(t *testing.T) {
	for i, r := range string(indexHTML) {
		if unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && r != '\n' && r != '\t') {
			t.Errorf("byte %d: invisible character U+%04X in index.html", i, r)
		}
	}
}

// Chain strings are shown as their bytes: every character that renders as
// nothing is escaped, not only controls and format characters (a variation
// selector or a grapheme joiner hides a difference just as well), and chain
// text laid out inline is fenced in isolates so a right-to-left string cannot
// reorder the values beside it.
func TestChainTextCannotHideOrReorder(t *testing.T) {
	page := string(indexHTML)
	ctl := regexp.MustCompile(`(?m)^const CTL = /.*/gu;$`).FindString(page)
	for _, class := range []string{`\p{Cc}`, `\p{Cf}`, `\p{Default_Ignorable_Code_Point}`} {
		if !strings.Contains(ctl, class) {
			t.Errorf("CTL %q does not escape %s", ctl, class)
		}
	}
	for _, rule := range []string{".v-str {", ".v-sym {", ".pv {", ".diff .p {"} {
		body := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(rule) + `[^}]*`).FindString(page)
		if !strings.Contains(body, "unicode-bidi: isolate") {
			t.Errorf("%s} is not isolated", rule)
		}
	}
}
