package releasebundle

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

const MaxArtifactBytes int64 = 256 << 20

var ErrArtifactIntegrity = errors.New("release artifact integrity failed")

func ArtifactPath(root, digest string) (string, error) {
	if root == "" || !ValidDigest(digest) {
		return "", errors.New("artifact storage or SHA256 is invalid")
	}
	return filepath.Join(root, digest), nil
}

func WriteArtifact(root, digest string, source io.Reader) (int64, error) {
	path, err := ArtifactPath(root, digest)
	if err != nil {
		return 0, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(root, ".artifact-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(source, MaxArtifactBytes+1))
	if err != nil {
		return 0, err
	}
	if n < 1 || n > MaxArtifactBytes {
		return 0, errors.New("artifact exceeds the 256 MiB budget or is empty")
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return 0, errors.New("artifact SHA256 mismatch")
	}
	if err = f.Chmod(0500); err != nil {
		return 0, err
	}
	if err = f.Sync(); err != nil {
		return 0, err
	}
	if err = f.Close(); err != nil {
		return 0, err
	}
	if err = os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return 0, err
	}
	_, err = VerifyArtifact(root, digest)
	return n, err
}

func VerifyArtifact(root, digest string) (int64, error) {
	path, err := ArtifactPath(root, digest)
	if err != nil {
		return 0, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxArtifactBytes {
		return 0, fmt.Errorf("%w: artifact must be a regular bounded file", ErrArtifactIntegrity)
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return 0, fmt.Errorf("%w: stored artifact SHA256 mismatch", ErrArtifactIntegrity)
	}
	return n, nil
}

func Artifact(ctx context.Context, s *store.Store, digest string) (model.ReleaseArtifact, error) {
	var a model.ReleaseArtifact
	var raw string
	err := s.DB.QueryRowContext(ctx, "SELECT sha256,size_bytes,build_json,created_ms FROM sf_release_artifacts WHERE sha256=$1", digest).Scan(&a.SHA256, &a.Size, &raw, &a.CreatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return a, store.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	err = json.Unmarshal([]byte(raw), &a.Build)
	return a, err
}

func RegisterArtifact(ctx context.Context, s *store.Store, root, digest string, build model.ProgramBuild, actor model.Actor, guards ...func(*store.Tx) error) (model.ReleaseArtifact, error) {
	n, err := VerifyArtifact(root, digest)
	if err != nil {
		return model.ReleaseArtifact{}, err
	}
	a := model.ReleaseArtifact{SHA256: digest, Size: n, Build: build, CreatedMS: s.CurrentTime().UnixMilli()}
	if build.Version == "" || (build.Program != "edge" && build.Program != "cloud") || build.GOOS == "" || build.GOARCH == "" || build.MigrationMinimum < 1 || build.MigrationMaximum < build.MigrationMinimum {
		return a, errors.New("invalid executable build metadata")
	}
	raw, err := json.Marshal(build)
	if err != nil {
		return a, err
	}
	err = s.Write(ctx, func(tx *store.Tx) error {
		for _, guard := range guards {
			if e := guard(tx); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO sf_release_artifacts(sha256,size_bytes,build_json,created_ms) VALUES($1,$2,$3,$4) ON CONFLICT(sha256) DO NOTHING", digest, n, string(raw), a.CreatedMS); e != nil {
			return e
		}
		var size int64
		var previous string
		if e := tx.QueryRowContext(ctx, "SELECT size_bytes,build_json FROM sf_release_artifacts WHERE sha256=$1", digest).Scan(&size, &previous); e != nil {
			return e
		}
		if size != n || previous != string(raw) {
			return fmt.Errorf("%w: artifact build metadata is immutable", store.ErrConflict)
		}
		return tx.Audit(actor, "release.artifact.register", build.Program, digest, map[string]any{"sha256": digest, "size": n, "build": build})
	})
	return a, err
}
