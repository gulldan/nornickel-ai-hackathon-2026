package embedding

import (
	"context"
	"errors"
	"testing"
)

type fakeEmbedder struct {
	got []string
	err error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.got = texts
	return [][]float32{{1, 2}}, f.err
}

func (f *fakeEmbedder) Dim() int { return 2 }

func TestAdapterEmbed(t *testing.T) {
	fake := &fakeEmbedder{}
	vecs, err := New(fake).Embed(context.Background(), []string{"a", "b"})
	if err != nil || len(vecs) != 1 || len(fake.got) != 2 {
		t.Fatalf("Embed = %v, %v (forwarded %d texts)", vecs, err, len(fake.got))
	}

	fake.err = errors.New("boom")
	if _, err := New(fake).Embed(context.Background(), nil); err == nil {
		t.Fatalf("Embed must propagate the client error")
	}
}
