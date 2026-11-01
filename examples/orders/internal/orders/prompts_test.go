package orders

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	genordersserver "github.com/goadesign/surla/examples/orders/gen/http/orders/server"
	genorders "github.com/goadesign/surla/examples/orders/gen/orders"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

// fakePrompts accepts templates other than "bad" for the prompt "known" and
// records them.
type fakePrompts struct {
	// set records the accepted templates.
	set []string
}

func TestOverridePromptOverHTTP(t *testing.T) {
	cases := []struct {
		name       string
		scope      string
		promptID   string
		template   string
		wantStatus int
		wantError  string
		wantSet    []string
	}{
		{"operator", "prompts:manage", "known", "New text.", http.StatusOK, "", []string{"New text."}},
		{"manager without the prompt scope", "orders:chat refunds:approve", "known", "New text.", http.StatusForbidden, "forbidden", nil},
		{"unregistered prompt", "prompts:manage", "other", "New text.", http.StatusNotFound, "unknown_prompt", nil},
		{"template does not render", "prompts:manage", "known", "bad", http.StatusUnprocessableEntity, "invalid_prompt", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prompts := &fakePrompts{}
			srv := httptest.NewServer(handler(New(nil, nil, nil, prompts, testKey)))
			defer srv.Close()
			body := strings.NewReader(fmt.Sprintf(`{"template":%q}`, c.template))
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/prompts/"+c.promptID, body)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token(t, testKey, "t1", "op1", c.scope, time.Hour))
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, c.wantStatus, resp.StatusCode)
			assert.Equal(t, c.wantError, resp.Header.Get("goa-error"), "the error name tells clients which failure it was")
			assert.Equal(t, c.wantSet, prompts.set)
			if c.wantStatus == http.StatusOK {
				var got genordersserver.OverridePromptResponseBody
				require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
				assert.Equal(t, "known", got.PromptID)
				assert.Equal(t, "version-of-New text.", got.Version)
			}
		})
	}
}

// handler serves svc's HTTP API the way cmd/orders mounts it.
func handler(svc *Service) http.Handler {
	mux := goahttp.NewMuxer()
	genordersserver.Mount(mux, genordersserver.New(genorders.NewEndpoints(svc), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil))
	return mux
}

// Override accepts templates for the prompt "known" other than "bad".
func (f *fakePrompts) Override(_ context.Context, promptID, template string) (string, error) {
	if promptID != "known" {
		return "", ErrPromptNotFound
	}
	if template == "bad" {
		return "", ErrInvalidPrompt
	}
	f.set = append(f.set, template)
	return "version-of-" + template, nil
}
