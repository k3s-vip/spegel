package containerd

import (
	"github.com/opencontainers/go-digest"

	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

type Index struct {
	tagNames map[string]digest.Digest
	contents map[digest.Digest]any
}

func NewIndex() *Index {
	return &Index{
		tagNames: map[string]digest.Digest{},
		contents: map[digest.Digest]any{},
	}
}

func (idx *Index) AddImage(img oci.Image) []store.Event {
	tagName, ok := img.TagName()
	if !ok {
		return nil
	}
	dgst, ok := idx.tagNames[tagName]
	if dgst != img.Digest {
		idx.tagNames[tagName] = img.Digest
	}
	if ok {
		return nil
	}
	return []store.Event{{Type: store.CreateEvent, Reference: tagName}}
}

func (idx *Index) RemoveImage(img oci.Image) []store.Event {
	tagName, ok := img.TagName()
	if !ok {
		return nil
	}
	if _, ok := idx.tagNames[tagName]; !ok {
		return nil
	}
	delete(idx.tagNames, tagName)
	return []store.Event{{Type: store.DeleteEvent, Reference: tagName}}
}

func (idx *Index) AddContent(dgst digest.Digest) []store.Event {
	if _, ok := idx.contents[dgst]; ok {
		return nil
	}
	idx.contents[dgst] = nil
	return []store.Event{{Type: store.CreateEvent, Digest: dgst}}
}

func (idx *Index) RemoveContent(dgst digest.Digest) []store.Event {
	if _, ok := idx.contents[dgst]; !ok {
		return nil
	}
	delete(idx.contents, dgst)
	return []store.Event{{Type: store.DeleteEvent, Digest: dgst}}
}
