package proxy

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type testFlusher struct {
	count int
}

func (f *testFlusher) Flush() {
	f.count++
}

func TestStreamKeepaliveWritesSSEComment(t *testing.T) {
	oldSettings := CurrentRuntimeSettings()
	ApplyRuntimeSettings(RuntimeSettings{
		ClientCompatMode:               oldSettings.ClientCompatMode,
		CodexMinCLIVersion:             oldSettings.CodexMinCLIVersion,
		StreamFlushPolicy:              oldSettings.StreamFlushPolicy,
		StreamFlushIntervalMS:          oldSettings.StreamFlushIntervalMS,
		StreamIdleTimeoutSeconds:       oldSettings.StreamIdleTimeoutSeconds,
		StreamKeepaliveIntervalSeconds: 1,
	})
	t.Cleanup(func() { ApplyRuntimeSettings(oldSettings) })

	var body bytes.Buffer
	flusher := &testFlusher{}
	writer := newStreamFlushWriter(&body, flusher)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := startStreamKeepalive(ctx, writer)
	time.Sleep(1100 * time.Millisecond)
	if err := stop(); err != nil {
		t.Fatalf("stop keepalive: %v", err)
	}

	if !strings.Contains(body.String(), ": codex2api-keepalive\n\n") {
		t.Fatalf("keepalive comment was not written; body=%q", body.String())
	}
	if flusher.count == 0 {
		t.Fatal("heartbeat should flush downstream writer")
	}
}

func TestStreamKeepaliveCanBeDisabled(t *testing.T) {
	oldSettings := CurrentRuntimeSettings()
	ApplyRuntimeSettings(RuntimeSettings{
		ClientCompatMode:               oldSettings.ClientCompatMode,
		CodexMinCLIVersion:             oldSettings.CodexMinCLIVersion,
		StreamFlushPolicy:              oldSettings.StreamFlushPolicy,
		StreamFlushIntervalMS:          oldSettings.StreamFlushIntervalMS,
		StreamIdleTimeoutSeconds:       oldSettings.StreamIdleTimeoutSeconds,
		StreamKeepaliveIntervalSeconds: 0,
	})
	t.Cleanup(func() { ApplyRuntimeSettings(oldSettings) })

	var body bytes.Buffer
	flusher := &testFlusher{}
	writer := newStreamFlushWriter(&body, flusher)

	stop := startStreamKeepalive(context.Background(), writer)
	if err := stop(); err != nil {
		t.Fatalf("stop keepalive: %v", err)
	}
	if body.Len() != 0 {
		t.Fatalf("disabled keepalive should not write; body=%q", body.String())
	}
}

func TestRuntimeStreamSafetySettingsCanBeDisabledAndClamped(t *testing.T) {
	settings := NormalizeRuntimeSettings(RuntimeSettings{
		ClientCompatMode:               ClientCompatModePreserve,
		CodexMinCLIVersion:             "0.118.0",
		StreamFlushPolicy:              StreamFlushPolicyImmediate,
		StreamFlushIntervalMS:          20,
		StreamIdleTimeoutSeconds:       0,
		StreamKeepaliveIntervalSeconds: 0,
	})
	if settings.StreamIdleTimeoutSeconds != 0 || settings.StreamKeepaliveIntervalSeconds != 0 {
		t.Fatalf("zero should disable stream safety timers: %+v", settings)
	}

	settings = NormalizeRuntimeSettings(RuntimeSettings{
		ClientCompatMode:               ClientCompatModePreserve,
		CodexMinCLIVersion:             "0.118.0",
		StreamFlushPolicy:              StreamFlushPolicyImmediate,
		StreamFlushIntervalMS:          20,
		StreamIdleTimeoutSeconds:       9999,
		StreamKeepaliveIntervalSeconds: 9999,
	})
	if settings.StreamIdleTimeoutSeconds != maxStreamIdleTimeoutSeconds {
		t.Fatalf("idle timeout = %d, want clamp %d", settings.StreamIdleTimeoutSeconds, maxStreamIdleTimeoutSeconds)
	}
	if settings.StreamKeepaliveIntervalSeconds != maxStreamKeepaliveIntervalSeconds {
		t.Fatalf("keepalive interval = %d, want clamp %d", settings.StreamKeepaliveIntervalSeconds, maxStreamKeepaliveIntervalSeconds)
	}
}

func TestWriteResponsesStreamEventBuffersPreamble(t *testing.T) {
	var body bytes.Buffer
	writer := newStreamFlushWriter(&body, &testFlusher{})
	var pending [][]byte
	committed := false

	created := []byte(`{"type":"response.created"}`)
	if err := writeResponsesStreamEvent(writer, &pending, &committed, "response.created", created); err != nil {
		t.Fatalf("write preamble: %v", err)
	}
	if body.Len() != 0 {
		t.Fatalf("preamble should be buffered before committed output; body=%q", body.String())
	}
	if committed {
		t.Fatal("preamble should not commit the downstream stream")
	}

	delta := []byte(`{"type":"response.output_text.delta","delta":"hi"}`)
	if err := writeResponsesStreamEvent(writer, &pending, &committed, "response.output_text.delta", delta); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	bodyText := body.String()
	if !strings.Contains(bodyText, "data: "+string(created)+"\n\n") ||
		!strings.Contains(bodyText, "data: "+string(delta)+"\n\n") {
		t.Fatalf("expected buffered preamble then delta, body=%q", bodyText)
	}
	if !committed {
		t.Fatal("non-preamble event should commit the downstream stream")
	}
}

func TestFinishResponsesStreamFlushesPreambleBeforeSyntheticFailure(t *testing.T) {
	var body bytes.Buffer
	writer := newStreamFlushWriter(&body, &testFlusher{})
	pending := [][]byte{[]byte(`{"type":"response.created"}`)}
	committed := false

	payload, err := finishResponsesStream(writer, nil, &pending, &committed, false, nil, nil, nil, true)
	if err != nil {
		t.Fatalf("finishResponsesStream returned error: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("expected synthetic response.failed payload")
	}

	bodyText := body.String()
	createdAt := strings.Index(bodyText, `"type":"response.created"`)
	failedAt := strings.Index(bodyText, `"type":"response.failed"`)
	if createdAt < 0 || failedAt < 0 || createdAt > failedAt {
		t.Fatalf("expected preamble before synthetic failure, body=%q", bodyText)
	}
	if !committed {
		t.Fatal("synthetic failure should commit the downstream stream")
	}
}
