package conversion

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/gateway"
	httpingress "github.com/nyroway/nyro/go/internal/llm/ingress/http"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/protocol/anthropic/messages"
	"github.com/nyroway/nyro/go/internal/llm/protocol/gemini/generatecontent"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/chatcompletions"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/embeddings"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/responses"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// routeModel is the client-facing model every scenario request uses; the route
// maps it to the upstream's real target model (recorded in the cassette so
// record and replay agree).
const routeModel = "conversion-test-model"

func testProtocolCatalog(t *testing.T) *protocol.Catalog {
	t.Helper()
	catalog, err := protocol.NewCatalog(
		[]protocol.IngressCodec{
			chatcompletions.NewIngress(),
			responses.NewIngress(),
			embeddings.NewIngress(),
			messages.NewIngress(),
			generatecontent.NewIngress(),
		},
		[]protocol.EgressCodec{
			chatcompletions.NewEgress(),
			responses.NewEgress(),
			embeddings.NewEgress(),
			messages.NewEgress(),
			generatecontent.NewEgress(),
		},
	)
	if err != nil {
		t.Fatalf("protocol catalog: %v", err)
	}
	return catalog
}

func testProviderCatalog(t *testing.T) *provider.Catalog {
	t.Helper()
	catalog, err := provider.NewCatalog(
		provider.Generic(), provider.OpenAI(), provider.Anthropic(),
		provider.Gemini(), provider.DeepSeek(), provider.OpenRouter(),
	)
	if err != nil {
		t.Fatalf("provider catalog: %v", err)
	}
	return catalog
}

// Inbound is the client-facing protocol: its protocol id (used in golden paths)
// and the ingress path requests are POSTed to.
type Inbound struct {
	Name string // inbound protocol id, e.g. "anthropic-messages"
	Path string // ingress path, e.g. "/v1/messages"
	// StreamPath, when set, is the ingress path used for streaming scenarios.
	// Gemini encodes the action in the path (:streamGenerateContent) rather than
	// a body flag; other protocols leave this empty and stream via the body.
	StreamPath string
}

// Outbound is the upstream protocol a cell translates to: the provider id (auth
// scheme), the protocol id (codec + golden/cassette label), and the expected
// outbound request path.
type Outbound struct {
	Provider string // provider id, e.g. "openai"
	Protocol string // upstream protocol id, e.g. "openai-chatcompletions"
	Path     string // expected outbound request path, e.g. "/v1/chat/completions"
}

// Cell is one inbound×outbound square of the conversion matrix.
type Cell struct {
	In  Inbound
	Out Outbound
}

// dir is the golden-directory label, using full protocol ids on both sides
// (e.g. "anthropic-messages__openai-chatcompletions") so it is unambiguous and matches the
// cassette tree, which is keyed by protocol id.
func (c Cell) dir() string { return c.In.Name + "__" + c.Out.Protocol }

// Scenario is a client request exercised across cells. Request is the wire body
// in the cell's inbound protocol; its model must be routeModel.
type Scenario struct {
	Name    string
	Request string
	Stream  bool
}

func recording() bool { return os.Getenv("NYRO_TEST_RECORD") != "" }

// recordProvider is the provider id (auth scheme + preset) to record against.
// NYRO_TEST_PROVIDER overrides the cell's nominal provider so a cell can be
// recorded through an aggregator that speaks the same wire protocol — e.g.
// recording openai-chatcompletions / openai-responses / anthropic-messages cassettes all
// through OpenRouter (Bearer auth). Outside record mode it is the cell's own
// provider (auth is irrelevant on replay anyway).
func recordProvider(cell Cell) string {
	if p := os.Getenv("NYRO_TEST_PROVIDER"); p != "" {
		return p
	}
	return cell.Out.Provider
}

// providerEnv resolves NYRO_TEST_<PROVIDER>_<KEY>, falling back to the generic
// NYRO_TEST_<KEY>. The per-provider form lets a single record run target
// multiple providers; the generic form supports recording one provider at a
// time (the three-var default: NYRO_TEST_API_KEY/BASE_URL/MODEL).
func providerEnv(provider, key string) string {
	if v := os.Getenv("NYRO_TEST_" + strings.ToUpper(provider) + "_" + key); v != "" {
		return v
	}
	return os.Getenv("NYRO_TEST_" + key)
}

// upstreamFor returns the capturing transport plus the upstream target model,
// base URL, and api key for a cell/scenario. In record mode it hits the live
// provider (env-configured) and writes the cassette; in replay mode it serves
// the committed cassette offline (skipping if absent).
func upstreamFor(t *testing.T, cell Cell, scenario string) (tr capturingTransport, model, baseURL, apiKey string) {
	t.Helper()
	casPath := filepath.Join("testdata", "cassettes", cell.Out.Protocol, scenario+".json")

	if recording() {
		prov := recordProvider(cell)
		baseURL = providerEnv(prov, "BASE_URL")
		apiKey = providerEnv(prov, "API_KEY")
		model = providerEnv(prov, "MODEL")
		if baseURL == "" || apiKey == "" || model == "" {
			t.Fatalf("record mode: set NYRO_TEST[_%s]_{BASE_URL,API_KEY,MODEL}",
				strings.ToUpper(prov))
		}
		return &recordTransport{real: http.DefaultTransport, path: casPath}, model, baseURL, apiKey
	}

	cas, err := loadCassette(casPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skipf("no cassette %s — record via `make go-conversion-update`", casPath)
		}
		t.Fatalf("load cassette: %v", err)
	}
	model = cas.Request.Model
	if model == "" {
		model = routeModel
	}
	return &replayTransport{cas: cas}, model, "https://replay.invalid", "replay-key"
}

// buildGateway assembles a storage-less gateway with one upstream (cell.Out) and
// a route mapping routeModel to it, then injects the capturing transport.
func buildGateway(t *testing.T, cell Cell, tr http.RoundTripper, baseURL, apiKey, upstreamModel string) *gateway.Gateway {
	t.Helper()
	core := memory.New().Storage()
	up, err := core.Upstreams().Create(storage.CreateUpstream{
		Name:            "conv-" + cell.Out.Protocol,
		Provider:        recordProvider(cell),
		Protocol:        cell.Out.Protocol,
		BaseURL:         baseURL,
		CredentialsJSON: fmt.Appendf(nil, `{"api_key":%q}`, apiKey),
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}
	if _, err := core.Routes().Create(storage.CreateRoute{
		Model:     routeModel,
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: upstreamModel}},
	}); err != nil {
		t.Fatalf("create route: %v", err)
	}
	gw := gateway.NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	gw.UpstreamTransport = tr
	if err := storage.LoadAndSwap(gw.Cache, core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	return gw
}

func buildHandler(t *testing.T, gw *gateway.Gateway) http.Handler {
	t.Helper()
	handler, err := httpingress.New(gw.Protocols, gw, httpingress.Options{})
	if err != nil {
		t.Fatalf("compose LLM HTTP ingress: %v", err)
	}
	return handler
}

// RunCell drives one cell×scenario through the real router and asserts the
// request-translation (client→upstream) and response-translation
// (upstream→client) directions against golden files.
func RunCell(t *testing.T, cell Cell, sc Scenario) {
	t.Helper()
	tr, model, baseURL, apiKey := upstreamFor(t, cell, sc.Name)
	gw := buildGateway(t, cell, tr, baseURL, apiKey, model)
	router := buildHandler(t, gw)

	inPath := cell.In.Path
	if sc.Stream && cell.In.StreamPath != "" {
		inPath = cell.In.StreamPath
	}
	req := httptest.NewRequest(http.MethodPost, inPath, strings.NewReader(sc.Request))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cell %s/%s: status=%d body=%s", cell.dir(), sc.Name, rec.Code, rec.Body.String())
	}

	// Request direction: what the gateway sent upstream.
	verb, path, outBody := tr.outboundRequest()
	if verb != http.MethodPost {
		t.Errorf("outbound method=%s, want POST", verb)
	}
	// Suffix match, not equality: the captured path includes the provider base
	// URL's own prefix (e.g. OpenRouter's /api/v1), which differs between the
	// record base URL and the replay placeholder. The codec-produced path is the
	// suffix.
	if cell.Out.Path != "" && !strings.HasSuffix(path, cell.Out.Path) {
		t.Errorf("outbound path=%s, want suffix %s", path, cell.Out.Path)
	}
	reqGot, err := canonJSON(outBody)
	if err != nil {
		t.Fatalf("canon outbound request: %v (body=%s)", err, outBody)
	}
	assertGolden(t, goldenPath(cell, sc, "request"), reqGot)

	// Response direction: what the client received back.
	if sc.Stream {
		respGot, err := canonSSE(rec.Body.String())
		if err != nil {
			t.Fatalf("canon client stream: %v", err)
		}
		assertGolden(t, goldenPath(cell, sc, "stream"), respGot)
	} else {
		respGot, err := canonJSON(rec.Body.Bytes())
		if err != nil {
			t.Fatalf("canon client response: %v (body=%s)", err, rec.Body.String())
		}
		assertGolden(t, goldenPath(cell, sc, "response"), respGot)
	}
}

func goldenPath(cell Cell, sc Scenario, direction string) string {
	return filepath.Join("testdata", "golden", cell.dir(), sc.Name+"."+direction+".golden")
}
