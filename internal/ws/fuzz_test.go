package ws

import (
	"encoding/json"
	"testing"
)

func FuzzDecodeMessages(f *testing.F) {
	f.Add([]byte(`{"type":"fetch","id":"job_1","url":"https://example.com"}`))
	f.Add([]byte(`{"type":"result","id":"job_1","status":200}`))
	f.Add([]byte(`{"type":"fetch_error","id":"job_1","error":"timeout"}`))
	f.Add([]byte(`{"type":"ping"}`))
	f.Add([]byte(`{"type":"pong"}`))

	f.Fuzz(func(t *testing.T, payload []byte) {
		var env envelope
		_ = json.Unmarshal(payload, &env)
		switch env.Type {
		case "fetch":
			var msg FetchRequest
			_ = json.Unmarshal(payload, &msg)
		case "result":
			var msg FetchResult
			_ = json.Unmarshal(payload, &msg)
		case "fetch_error":
			var msg FetchError
			_ = json.Unmarshal(payload, &msg)
		case "ping":
			var msg PingMessage
			_ = json.Unmarshal(payload, &msg)
		case "pong":
			var msg PongMessage
			_ = json.Unmarshal(payload, &msg)
		}
	})
}
