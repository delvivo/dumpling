package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/delvivo/dumpling/internal/store"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3PartSize bounds memory of streaming uploads of unknown size: minio-go buffers one part
// (default sizing for unknown length would buffer ~525 MiB). 64 MiB × 10 000 parts = 625 GiB max artifact.
const s3PartSize = 64 << 20

type s3b struct {
	ctx    context.Context
	c      *minio.Client
	bucket string
	prefix string
}

func openS3(ctx context.Context, cfg *store.S3Config) (Backend, error) {
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "s3.amazonaws.com"
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	c, err := minio.New(endpoint, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure:       cfg.UseTLS || cfg.Endpoint == "",
		Region:       cfg.Region,
		BucketLookup: lookup,
		MaxRetries:   3,
	})
	if err != nil {
		return nil, err
	}
	return &s3b{ctx: ctx, c: c, bucket: cfg.Bucket, prefix: cfg.Prefix}, nil
}

func (b *s3b) key(rel string) string {
	if b.prefix == "" {
		return rel
	}
	return b.prefix + "/" + rel
}

// notExist maps S3 "no such key" to fs.ErrNotExist.
func notExist(err error) error {
	if err == nil {
		return nil
	}
	if r := minio.ToErrorResponse(err); r.Code == "NoSuchKey" || r.StatusCode == 404 && r.Code != "NoSuchBucket" {
		return fmt.Errorf("%w: %v", fs.ErrNotExist, err)
	}
	return err
}

type s3Upload struct {
	pw   *io.PipeWriter
	res  chan error
	done bool
}

// Create streams to PutObject through a pipe. The object becomes visible only when the (multipart)
// upload completes; a failed upload is aborted by minio-go, leaving nothing listable.
func (b *s3b) Create(rel string) (Upload, error) {
	key := b.key(rel)
	// ponytail: stat-then-put race; names embed the operation id. Upgrade: If-None-Match: * once all
	// target providers support conditional writes.
	if _, err := b.c.StatObject(b.ctx, b.bucket, key, minio.StatObjectOptions{}); err == nil {
		return nil, fmt.Errorf("object %s already exists", key)
	} else if err = notExist(err); !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	pr, pw := io.Pipe()
	u := &s3Upload{pw: pw, res: make(chan error, 1)}
	go func() {
		_, err := b.c.PutObject(b.ctx, b.bucket, key, pr, -1, minio.PutObjectOptions{PartSize: s3PartSize})
		pr.CloseWithError(err) // unblock writers if the upload failed early
		u.res <- err
	}()
	return u, nil
}

func (u *s3Upload) Write(p []byte) (int, error) { return u.pw.Write(p) }

func (u *s3Upload) Commit() error {
	u.done = true
	u.pw.Close()
	return <-u.res
}

func (u *s3Upload) Abort() {
	if u.done {
		return
	}
	u.done = true
	u.pw.CloseWithError(fmt.Errorf("upload aborted"))
	<-u.res
}

type s3Reader struct{ *minio.Object }

func (b *s3b) Open(rel string) (io.ReadCloser, error) {
	o, err := b.c.GetObject(b.ctx, b.bucket, b.key(rel), minio.GetObjectOptions{})
	if err != nil {
		return nil, notExist(err)
	}
	if _, err := o.Stat(); err != nil { // GetObject is lazy: surface missing keys now
		o.Close()
		return nil, notExist(err)
	}
	return s3Reader{o}, nil
}

func (b *s3b) Stat(rel string) (int64, error) {
	info, err := b.c.StatObject(b.ctx, b.bucket, b.key(rel), minio.StatObjectOptions{})
	if err != nil {
		return 0, notExist(err)
	}
	return info.Size, nil
}

func (b *s3b) Remove(rel string) error {
	if _, err := b.Stat(rel); err != nil { // S3 delete of a missing key succeeds; keep os.Remove semantics
		return err
	}
	return b.c.RemoveObject(b.ctx, b.bucket, b.key(rel), minio.RemoveObjectOptions{})
}

func (b *s3b) listPrefix() string {
	if b.prefix == "" {
		return ""
	}
	return b.prefix + "/"
}

func (b *s3b) List(suffix string) ([]string, error) {
	out := []string{}
	pre := b.listPrefix()
	for o := range b.c.ListObjects(b.ctx, b.bucket, minio.ListObjectsOptions{Prefix: pre, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		if strings.HasSuffix(o.Key, suffix) {
			out = append(out, strings.TrimPrefix(o.Key, pre))
		}
	}
	return out, nil
}

func (b *s3b) Cleanup() error {
	var first error
	for u := range b.c.ListIncompleteUploads(b.ctx, b.bucket, b.listPrefix(), true) {
		if u.Err != nil {
			return u.Err
		}
		if err := b.c.RemoveIncompleteUpload(b.ctx, b.bucket, u.Key); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (b *s3b) Close() error { return nil }
