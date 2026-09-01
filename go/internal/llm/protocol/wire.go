package protocol

type IngressRequest struct {
	Body   []byte
	Params map[string]string
}

type WireRequest struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    []byte
	Stream  bool
}

type WireResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type Event struct {
	Event string
	Data  string
}

func (e Event) Bytes() []byte {
	var out []byte
	if e.Event != "" {
		out = append(out, "event: "...)
		out = append(out, e.Event...)
		out = append(out, '\n')
	}
	out = append(out, "data: "...)
	out = append(out, e.Data...)
	out = append(out, '\n', '\n')
	return out
}
