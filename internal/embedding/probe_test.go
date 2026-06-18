package embedding

import (
	"context"
	"testing"
)

type mockEmbedder struct {
	available bool
	vecs      [][]float32
	err       error
	model     string
	dim       int
}

func (m *mockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return m.vecs, m.err
}
func (m *mockEmbedder) Model() string   { return m.model }
func (m *mockEmbedder) Dim() int        { return m.dim }
func (m *mockEmbedder) Available() bool { return m.available }

func TestProbeEmbedder_Success(t *testing.T) {
	emb := &mockEmbedder{
		available: true,
		model:     "test-model",
		dim:       1024,
		vecs:      [][]float32{make([]float32, 1024)},
	}
	result := ProbeEmbedder(context.Background(), emb)
	if !result.Available() {
		t.Fatal("probe should return available embedder on success")
	}
}

func TestProbeEmbedder_NotConfigured(t *testing.T) {
	emb := &mockEmbedder{available: false}
	result := ProbeEmbedder(context.Background(), emb)
	if result.Available() {
		t.Fatal("probe should return nil embedder when not configured")
	}
}

func TestProbeEmbedder_APIError(t *testing.T) {
	emb := &mockEmbedder{
		available: true,
		model:     "test-model",
		dim:       1024,
		err:       context.DeadlineExceeded,
	}
	result := ProbeEmbedder(context.Background(), emb)
	if result.Available() {
		t.Fatal("probe should return nil embedder on API error")
	}
}

func TestProbeEmbedder_WrongDim(t *testing.T) {
	emb := &mockEmbedder{
		available: true,
		model:     "test-model",
		dim:       1024,
		vecs:      [][]float32{make([]float32, 512)},
	}
	result := ProbeEmbedder(context.Background(), emb)
	if result.Available() {
		t.Fatal("probe should return nil embedder on wrong dimensions")
	}
}
