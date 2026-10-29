package reverse

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPClientRejectsInvalidAccountProxyWithoutFallingBack(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	for _, proxy := range []string{"invalid-proxy", "file://example.com", "http://%zz"} {
		_, err := HTTPClient(proxy).Get(server.URL)
		require.ErrorContains(t, err, "invalid reverse account proxy URL")
	}
	require.False(t, called)
}

func TestHTTPClientDoesNotForwardCredentialsAcrossRedirects(t *testing.T) {
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			redirected = true
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer server.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer test-access")
	resp, err := HTTPClient("").Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.False(t, redirected)
}
