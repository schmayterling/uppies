package web

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAPITokenManagementNeedsAnAdminSessionAndSameOrigin(t *testing.T) {
	site := newSite(t)

	status, _ := site.get(t, site.anonymous, "/admin/api-tokens")
	if status != http.StatusSeeOther {
		t.Fatalf("token list without admin session: got %d, want 303", status)
	}

	form := url.Values{"name": {"CI"}, "lifetime": {"30"}}
	if resp := site.post(t, site.anonymous, "/admin/api-tokens", form, nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create without admin session: got %d, want 303", resp.StatusCode)
	}

	foreignOrigin := http.Header{"Origin": {"https://example.net"}, "Sec-Fetch-Site": {"cross-site"}}
	if resp := site.post(t, site.admin, "/admin/api-tokens", form, foreignOrigin); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin create: got %d, want 403", resp.StatusCode)
	}
	if resp := site.post(t, site.admin, "/admin/api-tokens/1/permissions", form, foreignOrigin); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin permission change: got %d, want 403", resp.StatusCode)
	}
	if resp := site.post(t, site.admin, "/admin/api-tokens/1/revoke", nil, foreignOrigin); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin revoke: got %d, want 403", resp.StatusCode)
	}
}

func createAPIToken(t *testing.T, site *site, permissions ...string) string {
	t.Helper()
	form := url.Values{"name": {"CI"}, "lifetime": {"30"}, "permission": permissions}
	resp, err := site.admin.PostForm(site.server.URL+"/admin/api-tokens", form)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create token: got %d and cache-control %q, want 201 and no-store", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	_, rest, ok := strings.Cut(string(body), "<code>")
	token, _, closed := strings.Cut(rest, "</code>")
	if !ok || !closed || !strings.HasPrefix(token, "uppies_") || len(token) != len("uppies_")+64 {
		t.Fatal("create token: expected the new credential in the response")
	}
	return token
}

func TestAPITokenSecretIsShownOnlyAfterCreation(t *testing.T) {
	site := newSite(t)
	form := url.Values{"name": {"CI"}, "lifetime": {"30"}, "permission": {"monitors:*"}}
	if resp := site.post(t, site.admin, "/admin/api-tokens", form, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("create with unknown permission: got %d, want 422", resp.StatusCode)
	}
	token := createAPIToken(t, site)
	status, body := site.get(t, site.admin, "/admin/api-tokens/1")
	if status != http.StatusOK || !strings.Contains(body, "CI") || strings.Contains(body, token) {
		t.Fatalf("read token metadata: got %d, want 200 with name and without the credential", status)
	}
	if strings.Contains(body, " checked") {
		t.Fatal("creating a token with zero selections granted a permission")
	}
	status, body = site.get(t, site.admin, "/admin/api-tokens")
	if status != http.StatusOK || strings.Contains(body, token) {
		t.Fatalf("list tokens: got %d, want 200 without the credential", status)
	}
}
