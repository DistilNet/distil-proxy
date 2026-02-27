package ws

const (
	DefaultProtocolVersion = "1.0.0"
	HeaderDistilKey        = "X-Distil-Key"
	HeaderDistilVersion    = "X-Distil-Version"
)

// FetchRequest is sent from server to daemon.
type FetchRequest struct {
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	URL       string            `json:"url"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

// FetchResult is sent from daemon to server on successful fetch.
type FetchResult struct {
	Type      string            `json:"type"`
	ID        string            `json:"id"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	ElapsedMS int64             `json:"elapsed_ms"`
}

// FetchError is sent from daemon to server when fetch fails.
type FetchError struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

// PingMessage heartbeat frame.
type PingMessage struct {
	Type string `json:"type"`
}

// PongMessage heartbeat frame.
type PongMessage struct {
	Type string `json:"type"`
}

type envelope struct {
	Type string `json:"type"`
}
