// internal/toolbox/image.go
package toolbox

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed Dockerfile.base
var baseFS embed.FS

// BaseMovingTag is the stable name a repo's overlay writes in its FROM
// line. It is retagged to whatever BaseTag currently resolves to, so an
// overlay never has to name a hash.
const BaseMovingTag = "coop-tools:base"

// BaseTag names the base image by the hash of the Dockerfile that built
// it, so upgrading coop rebuilds automatically rather than running on a
// stale image. The superseded one survives until coop tools prune.
func BaseTag() string {
	return "coop-tools:base-" + shortHash(baseDockerfile())
}

func baseDockerfile() []byte {
	b, err := baseFS.ReadFile("Dockerfile.base")
	if err != nil {
		// Embedded at build time; unreachable short of a corrupt binary.
		panic("toolbox: missing embedded Dockerfile.base: " + err.Error())
	}
	return b
}

// OverlayPath is <repo>/.coop/tools.Dockerfile.
func OverlayPath(repo string) string {
	return filepath.Join(repo, ".coop", "tools.Dockerfile")
}

// ImageTag is the image a repo runs: its overlay's, or the base. The
// overlay's tag hashes the overlay *and* the base, so editing either
// rebuilds.
func ImageTag(repo string) (string, error) {
	overlay, err := os.ReadFile(OverlayPath(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return BaseTag(), nil
	}
	if err != nil {
		return "", err
	}
	return "coop-tools-" + Slug(repo) + ":" +
		shortHash(append(append([]byte{}, baseDockerfile()...), overlay...)), nil
}

// EnsureImage resolves the repo's image, building whatever is missing,
// and returns its tag. Building the base also moves BaseMovingTag, which
// is what an overlay's FROM line resolves through.
func EnsureImage(e Engine, repo string) (string, error) {
	if !haveImage(e, BaseTag()) {
		if err := buildBase(e); err != nil {
			return "", err
		}
	}
	// Idempotent and cheap: the moving tag can be stale from an earlier
	// coop even when the hash tag is present.
	if err := e.Run("tag", BaseTag(), BaseMovingTag); err != nil {
		return "", err
	}
	tag, err := ImageTag(repo)
	if err != nil {
		return "", err
	}
	if tag == BaseTag() || haveImage(e, tag) {
		return tag, nil
	}
	// The overlay's build context is the repo's .coop directory: an
	// overlay that COPYs from the repo would make the image depend on
	// working-tree state, so the context stays the directory the
	// Dockerfile lives in.
	if err := e.Run("build", "-t", tag, "-f", OverlayPath(repo),
		filepath.Dir(OverlayPath(repo))); err != nil {
		return "", fmt.Errorf("overlay build: %w", err)
	}
	return tag, nil
}

func haveImage(e Engine, tag string) bool {
	// A real "image inspect" prints a JSON blob on success and never
	// succeeds silently, so require non-empty output rather than just
	// err == nil — an unstubbed fake call otherwise reads as "present".
	out, err := e.Output("image", "inspect", tag)
	return err == nil && strings.TrimSpace(out) != ""
}

// buildBase pipes the embedded Dockerfile in on stdin ("-f -"), so no
// temporary file has to exist and the context stays empty.
func buildBase(e Engine) error {
	dir, err := os.MkdirTemp("", "coop-toolbox-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	df := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(df, baseDockerfile(), 0o644); err != nil {
		return err
	}
	if err := e.Run("build", "-t", BaseTag(), "-f", df, dir); err != nil {
		return fmt.Errorf("base build: %w", err)
	}
	return nil
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}
