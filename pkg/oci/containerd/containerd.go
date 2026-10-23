package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	client "github.com/containerd/containerd"
	eventtypes "github.com/containerd/containerd/api/events"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/events"
	"github.com/containerd/containerd/images"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/go-logr/logr"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"

	"github.com/kvick-org/pkg/option"

	"github.com/spegel-org/spegel/pkg/httpx"
	"github.com/spegel-org/spegel/pkg/oci"
	"github.com/spegel-org/spegel/pkg/store"
)

var _ oci.ImageLister = &Containerd{}
var _ store.Provider = &Containerd{}
var _ store.Watcher = &Containerd{}

type ContainerdConfig struct {
	Conn        net.Conn
	ContentPath string
	Filters     []oci.Filter
}

type ContainerdOption = option.Option[ContainerdConfig]

func WithContentPath(path string) ContainerdOption {
	return func(cfg *ContainerdConfig) error {
		cfg.ContentPath = path
		return nil
	}
}

func WithConnection(conn net.Conn) ContainerdOption {
	return func(cfg *ContainerdConfig) error {
		cfg.Conn = conn
		return nil
	}
}

func WithFilters(filters []oci.Filter) ContainerdOption {
	return func(cfg *ContainerdConfig) error {
		cfg.Filters = filters
		return nil
	}
}

type Containerd struct {
	client       *client.Client
	namespace    string
	manifestIdx  *lru.Cache[digest.Digest, []digest.Digest]
	mediaTypeIdx *lru.Cache[digest.Digest, string]
	contentPath  string
	filters      []oci.Filter
}

func NewContainerd(ctx context.Context, socketPath, namespace string, opts ...ContainerdOption) (*Containerd, error) {
	cfg := ContainerdConfig{}
	err := option.Apply(&cfg, opts...)
	if err != nil {
		return nil, err
	}
	if namespace == "" {
		return nil, errors.New("namespace must not be empty")
	}

	clientOpts := []client.ClientOpt{
		client.WithDefaultNamespace(namespace),
	}
	if cfg.Conn != nil {
		dialOpt := grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return cfg.Conn, nil
		})
		clientOpts = append(clientOpts, client.WithDialOpts([]grpc.DialOption{dialOpt}))
	}
	client, err := client.New(socketPath, clientOpts...)
	if err != nil {
		return nil, err
	}

	contentPath := cfg.ContentPath

	manifestIdx, err := lru.New[digest.Digest, []digest.Digest](2000)
	if err != nil {
		client.Close()
		return nil, err
	}
	mediaTypeIdx, err := lru.New[digest.Digest, string](100)
	if err != nil {
		client.Close()
		return nil, err
	}

	c := &Containerd{
		client:       client,
		namespace:    namespace,
		manifestIdx:  manifestIdx,
		mediaTypeIdx: mediaTypeIdx,
		contentPath:  contentPath,
		filters:      cfg.Filters,
	}
	return c, nil
}

func (c *Containerd) Close() error { return c.client.Close() }

func (c *Containerd) Name() string { return "containerd" }

func (c *Containerd) ListImages(ctx context.Context) ([]oci.Image, error) {
	cImgs, err := c.client.ImageService().List(ctx, `name~="^.+/"`)
	if err != nil {
		return nil, err
	}
	return deduplicateImages(ctx, cImgs, c.filters), nil
}

type imgEntry struct {
	img  oci.Image
	dgst digest.Digest
}

func deduplicateImages(ctx context.Context, cImgs []images.Image, filters []oci.Filter) []oci.Image {
	seen := make(map[digest.Digest]struct{}, len(cImgs))
	imgs := make([]oci.Image, 0, len(cImgs))
	dgstImgs := make([]imgEntry, 0, len(cImgs))
	for _, cImg := range cImgs {
		img, err := oci.ParseImage(cImg.Name, oci.WithDigest(cImg.Target.Digest))
		if err != nil {
			logr.FromContextOrDiscard(ctx).Error(err, "could not parse image name", "name", cImg.Name)
			continue
		}
		if oci.MatchesFilter(img.Reference, filters) {
			continue
		}
		if _, ok := img.TagName(); ok {
			seen[cImg.Target.Digest] = struct{}{}
			imgs = append(imgs, img)
			continue
		}
		dgstImgs = append(dgstImgs, imgEntry{img: img, dgst: cImg.Target.Digest})
	}
	for _, e := range dgstImgs {
		if _, ok := seen[e.dgst]; ok {
			continue
		}
		imgs = append(imgs, e.img)
	}
	return imgs
}

func (c *Containerd) Resolve(ctx context.Context, ref string) (digest.Digest, error) {
	cImg, err := c.client.ImageService().Get(ctx, ref)
	if errors.Is(err, errdefs.ErrNotFound) {
		return "", errors.Join(store.ErrNotFound, err)
	}
	if err != nil {
		return "", err
	}
	return cImg.Target.Digest, nil
}

func (c *Containerd) Descriptor(ctx context.Context, dgst digest.Digest) (store.Descriptor, error) {
	info, err := c.client.ContentStore().Info(ctx, dgst)
	if errors.Is(err, errdefs.ErrNotFound) {
		return store.Descriptor{}, errors.Join(store.ErrNotFound, err)
	}
	if err != nil {
		return store.Descriptor{}, err
	}

	mt, ok := c.mediaTypeIdx.Get(dgst)
	if !ok {
		mt, err = func() (string, error) {
			if info.Size > oci.ManifestMaxSize {
				return httpx.ContentTypeBinary, nil
			}
			rc, err := c.Open(ctx, dgst)
			if err != nil {
				return "", err
			}
			defer rc.Close()
			mt, err := oci.FingerprintMediaType(rc)
			if err != nil {
				return "", err
			}
			return mt, nil
		}()
		if err != nil {
			return store.Descriptor{}, err
		}
		c.mediaTypeIdx.Add(dgst, mt)
	}

	desc := store.Descriptor{
		Size:      info.Size,
		Digest:    dgst,
		MediaType: mt,
	}
	return desc, nil
}

func (c *Containerd) Open(ctx context.Context, dgst digest.Digest) (io.ReadSeekCloser, error) {
	if c.contentPath != "" {
		path := filepath.Join(c.contentPath, "blobs", dgst.Algorithm().String(), dgst.Encoded())
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(store.ErrNotFound, err)
		}
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if errors.Is(err, errdefs.ErrNotFound) {
		return nil, errors.Join(store.ErrNotFound, err)
	}
	if err != nil {
		return nil, err
	}
	return struct {
		io.ReadSeeker
		io.Closer
	}{
		ReadSeeker: io.NewSectionReader(ra, 0, ra.Size()),
		Closer:     ra,
	}, nil
}

func (c *Containerd) Watch(ctx context.Context) ([]store.Event, <-chan store.Event, error) {
	log := logr.FromContextOrDiscard(ctx)

	eventCh := make(chan store.Event, 128)
	subCtx, subCancel := context.WithCancel(ctx)
	eventFilters := []string{
		fmt.Sprintf(`namespace==%q,topic~="/images/create|/images/update",event.name~="^.+/"`, c.namespace),
		fmt.Sprintf(`namespace==%q,topic~="/images/delete"`, c.namespace),
		fmt.Sprintf(`namespace==%q,topic~="/content/delete"`, c.namespace),
	}
	envelopeCh, cErrCh := c.client.EventService().Subscribe(subCtx, eventFilters...)

	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		defer subCancel()
		for err := range cErrCh {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Error(err, "received containerd event error")
		}
	}()

	// Populate the content index.
	idx := NewIndex()
	initial := []store.Event{}

	imgs, err := c.ListImages(ctx)
	if err != nil {
		subCancel()
		<-subDone
		return nil, nil, err
	}
	for _, img := range imgs {
		initial = append(initial, idx.AddImage(img)...)
	}
	err = c.client.ContentStore().Walk(ctx, func(info content.Info) error {
		initial = append(initial, idx.AddContent(info.Digest)...)
		return nil
	})
	if err != nil {
		subCancel()
		<-subDone
		return nil, nil, err
	}

	select {
	case <-subCtx.Done():
		<-subDone
		return nil, nil, fmt.Errorf("event subscription terminated during initialization: %w", subCtx.Err())
	default:
	}

	go func() {
		defer close(eventCh)
		defer subCancel()
		for {
			select {
			case <-subCtx.Done():
				return
			case envelope, ok := <-envelopeCh:
				if !ok {
					return
				}
				events, err := c.handleEvent(subCtx, *envelope, idx)
				if err != nil {
					log.Error(err, "error when handling containerd event")
					continue
				}
				for _, event := range events {
					select {
					case eventCh <- event:
					case <-subCtx.Done():
						return
					}
				}
			}
		}
	}()

	return initial, eventCh, nil
}

func (c *Containerd) handleEvent(ctx context.Context, envelope events.Envelope, idx *Index) ([]store.Event, error) {
	if envelope.Event == nil {
		return nil, errors.New("envelope event cannot be nil")
	}
	evt, err := typeurl.UnmarshalAny(envelope.Event)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope event: %w", err)
	}

	switch e := evt.(type) {
	case *eventtypes.ImageCreate:
		return c.handleImageUpsert(ctx, idx, e.GetName(), envelope)
	case *eventtypes.ImageUpdate:
		return c.handleImageUpsert(ctx, idx, e.GetName(), envelope)
	case *eventtypes.ImageDelete:
		return c.handleImageDelete(ctx, idx, e.GetName())
	case *eventtypes.ContentDelete:
		return c.handleContentDelete(ctx, idx, e.GetDigest())
	default:
		return nil, fmt.Errorf("unsupported event type %T", evt)
	}
}

func (c *Containerd) handleImageUpsert(ctx context.Context, idx *Index, name string, envelope events.Envelope) ([]store.Event, error) {
	cImg, err := c.client.ImageService().Get(ctx, name)
	if err != nil {
		if errors.Is(err, errdefs.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !envelope.Timestamp.IsZero() && cImg.UpdatedAt.After(envelope.Timestamp) {
		logr.FromContextOrDiscard(ctx).Info("skipping image that was updated after event", "image", name)
		return nil, nil
	}
	img, err := oci.ParseImage(name, oci.WithDigest(cImg.Target.Digest))
	if err != nil {
		return nil, err
	}
	evts := idx.AddImage(img)
	digests, err := c.imageContentDigests(ctx, cImg.Target)
	if err != nil {
		logr.FromContextOrDiscard(ctx).Error(err, "failed to resolve image content, will retry on next image event", "image", name, "digest", cImg.Target.Digest)
		return evts, nil
	}
	for _, dgst := range digests {
		evts = append(evts, idx.AddContent(dgst)...)
	}
	return evts, nil
}

func (c *Containerd) handleImageDelete(ctx context.Context, idx *Index, name string) ([]store.Event, error) {
	if _, err := digest.Parse(name); err == nil {
		// cleanup by /content/delete
		return nil, nil
	}
	img, err := oci.ParseImage(name, oci.AllowTagOnly())
	if err != nil {
		return nil, err
	}
	return idx.RemoveImage(img), nil
}

func (c *Containerd) handleContentDelete(ctx context.Context, idx *Index, name string) ([]store.Event, error) {
	dgst, err := digest.Parse(name)
	if err != nil {
		return nil, err
	}
	c.manifestIdx.Remove(dgst)
	c.mediaTypeIdx.Remove(dgst)
	return idx.RemoveContent(dgst), nil
}

func (c *Containerd) imageContentDigests(ctx context.Context, target ocispec.Descriptor) ([]digest.Digest, error) {
	if v, ok := c.manifestIdx.Get(target.Digest); ok {
		return v, nil
	}
	raw, err := c.expandManifest(ctx, target, 0)
	if err != nil {
		return nil, err
	}
	seen := make(map[digest.Digest]struct{}, len(raw))
	dgsts := make([]digest.Digest, 0, len(raw))
	for _, dgst := range raw {
		if _, ok := seen[dgst]; ok {
			continue
		}
		seen[dgst] = struct{}{}
		dgsts = append(dgsts, dgst)
	}
	c.manifestIdx.Add(target.Digest, dgsts)
	return dgsts, nil
}

func (c *Containerd) expandManifest(ctx context.Context, desc ocispec.Descriptor, depth int) ([]digest.Digest, error) {
	if depth >= 8 {
		return nil, fmt.Errorf("manifest depth limit exceeded for %s", desc.Digest)
	}
	if desc.Size > oci.ManifestMaxSize {
		return nil, fmt.Errorf("content %s exceeds max manifest size %d", desc.Digest, oci.ManifestMaxSize)
	}
	children, err := images.Children(ctx, c.client.ContentStore(), desc)
	if err != nil {
		return nil, err
	}
	if len(children) == 0 {
		return []digest.Digest{desc.Digest}, nil
	}
	dgsts := make([]digest.Digest, 0, len(children)+1)
	dgsts = append(dgsts, desc.Digest)
	for _, child := range children {
		switch child.MediaType {
		case ocispec.MediaTypeImageIndex, images.MediaTypeDockerSchema2ManifestList,
			ocispec.MediaTypeImageManifest, images.MediaTypeDockerSchema2Manifest:
			sub, err := c.expandManifest(ctx, child, depth+1)
			if err != nil {
				return nil, err
			}
			dgsts = append(dgsts, sub...)
		default:
			dgsts = append(dgsts, child.Digest)
		}
	}
	return dgsts, nil
}
