package vendor

import (
	"encoding/json"
	"fmt"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// BuildRequest is the standard 7-step request pipeline:
// pre_request → normalize_tool_results → pre_encode → codec_encode →
// post_encode → auth_headers → build_url. Ported from
// provider/common/pipeline.rs.
func BuildRequest(v Vendor, req *ir.AiRequest, pctx *ProviderCtx, egress codec.EndpointHandler) (codec.OutboundRequest, error) {
	req.Model = pctx.ActualModel
	vctx := &VendorCtx{APIKey: pctx.APIKey, ActualModel: pctx.ActualModel}

	// Step 1: pre_request hook (Ollama probes /api/show — not yet ported; no-op via PreEncode).

	// Step 2: normalize tool results (codec-level — not yet ported; skipped).

	// Step 3: pre_encode hook.
	if err := v.PreEncode(vctx, req); err != nil {
		return codec.OutboundRequest{}, fmt.Errorf("pre_encode: %w", err)
	}

	// Step 4: codec encode.
	outbound, err := egress.MakeRequestEncoder().Encode(req)
	if err != nil {
		return codec.OutboundRequest{}, fmt.Errorf("codec encode: %w", err)
	}

	// Step 5: post_encode hook (mutate body + headers).
	if len(outbound.Body) > 0 {
		newBody, newHeaders, perr := v.PostEncode(vctx, outbound.Body, outbound.Headers)
		if perr != nil {
			return codec.OutboundRequest{}, fmt.Errorf("post_encode: %w", perr)
		}
		outbound.Body = newBody
		outbound.Headers = newHeaders
	}

	// Step 6: auth headers (vendor-specific).
	authHeaders := v.AuthHeaders(vctx)
	if authHeaders != nil {
		if outbound.Headers == nil {
			outbound.Headers = make(map[string]string)
		}
		for k, val := range authHeaders {
			outbound.Headers[k] = val
		}
	}

	// Step 7: build URL (vendor-specific).
	outbound.Path = v.BuildURL(vctx, pctx.Provider.BaseURL, outbound.Path)

	return outbound, nil
}

// ParseResponse is the standard 4-step response pipeline:
// pre_parse → codec_parse → reasoning_normalization → post_parse.
func ParseResponse(v Vendor, body []byte, pctx *ProviderCtx, egress codec.EndpointHandler) (*ir.AiResponse, error) {
	vctx := &VendorCtx{APIKey: pctx.APIKey, ActualModel: pctx.ActualModel}

	// Step 1: pre_parse hook.
	raw := json.RawMessage(body)
	preParsed, err := v.PreParse(vctx, raw)
	if err != nil {
		return nil, fmt.Errorf("pre_parse: %w", err)
	}

	// Step 2: codec parse.
	resp, err := egress.MakeResponseDecoder().Parse(preParsed)
	if err != nil {
		return nil, fmt.Errorf("codec parse: %w", err)
	}

	// Step 3: reasoning normalization (codec-level — not yet ported).

	// Step 4: post_parse hook.
	if err := v.PostParse(vctx, resp); err != nil {
		return nil, fmt.Errorf("post_parse: %w", err)
	}

	return resp, nil
}
