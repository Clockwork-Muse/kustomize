# OCI Registry Support

Kustomize can pull kustomization directories from OCI (Open Container Initiative)
registries and push them for sharing. This enables versioned, signed distribution
of kustomization bundles through standard container registries.

## URL Format

OCI references use the `oci://` scheme:

```
oci://registry/repository:tag
oci://registry/repository@sha256:digest
oci://registry/repository:tag//path/in/artifact
```

Components:
- **registry** — the OCI registry hostname (e.g., `ghcr.io`, `docker.io`, `localhost:5000`)
- **repository** — the image repository path (e.g., `myorg/my-kustomization`)
- **tag or digest** — a version identifier (e.g., `:v1.0.0`, `@sha256:abc...`)
- **path** (optional) — a subdirectory within the artifact, separated by `//`

## Building from an OCI Registry

Use an OCI reference anywhere you'd use a git URL or local path:

```yaml
# kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
- oci://ghcr.io/myorg/base-app:v1.0.0
```

Then build normally:

```bash
kustomize build .
```

You can also build directly from an OCI reference:

```bash
kustomize build oci://ghcr.io/myorg/base-app:v1.0.0
```

### With a subdirectory

If the artifact contains multiple kustomization roots, specify a path:

```bash
kustomize build oci://ghcr.io/myorg/platform:v2.0.0//overlays/production
```

## Publishing to an OCI Registry

Package and push a kustomization directory to a registry:

```bash
# Publish the current directory
kustomize publish ghcr.io/myorg/base-app:v1.0.0

# Publish a specific directory
kustomize publish ghcr.io/myorg/base-app:v1.0.0 --path ./base

# Publish to multiple targets
kustomize publish ghcr.io/myorg/base-app:v1.0.0 ghcr.io/myorg/base-app:latest-release --path ./base
```

The publish command:
- Requires an explicit tag (rejects implicit `latest`)
- Validates the kustomization file before pushing
- Ensures all referenced paths are local to the directory
- Uses Docker credentials from `~/.docker/config.json`

## Localizing OCI References

`kustomize localize` downloads OCI artifacts and rewrites references to local paths:

```bash
kustomize localize oci://ghcr.io/myorg/base-app:v1.0.0 ./localized
```

This creates a local copy at:
```
./localized/localized-files/ghcr.io/myorg/base-app/v1.0.0/
```

OCI references within kustomization files (e.g., in `resources`) are also
localized recursively. An explicit tag or digest is required — implicit `latest`
is rejected to ensure reproducible builds.

## Authentication

Kustomize uses Docker credentials for registry authentication. Configure them
using standard Docker methods:

```bash
# Docker login (writes to ~/.docker/config.json)
docker login ghcr.io

# Or use a credential helper
echo '{"credHelpers":{"ghcr.io":"gh"}}' > ~/.docker/config.json
```

### Environment Variable Authentication

For CI/CD environments without Docker, use environment variables:

```bash
export KUSTOMIZE_OCI_USERNAME=myuser
export KUSTOMIZE_OCI_PASSWORD=mytoken

# These are used for both pull and push
kustomize build oci://registry/repo:v1.0.0
kustomize publish registry/repo:v1.0.0
```

The env vars take priority over Docker config. No Docker installation is needed.

## SemVer Tag Resolution

Instead of a literal tag, you can use a semver constraint:

```yaml
resources:
- oci://ghcr.io/myorg/base:>=1.0.0 <2.0.0
```

Kustomize will list all tags from the registry, parse them as semantic versions,
and resolve to the **highest** version matching the constraint. Supported constraint
syntax (from [blang/semver](https://github.com/blang/semver)):

```
>=1.0.0          # v1.0.0 or higher
>=1.0.0 <2.0.0   # v1.x only
~1.2.0           # >=1.2.0 <1.3.0
^1.2.0           # >=1.2.0 <2.0.0
```

## Publish Annotations

Record source provenance when publishing:

```bash
kustomize publish ghcr.io/myorg/app:v1.0.0 \
  --source https://github.com/myorg/app \
  --revision main/abc123def
```

This sets standard OCI annotations on the manifest:
- `org.opencontainers.image.created` — always set (UTC RFC3339)
- `org.opencontainers.image.source` — from `--source`
- `org.opencontainers.image.revision` — from `--revision`

## File Exclusions

Exclude files from published artifacts using a `.kustomizeignore` file
(gitignore glob format) in the directory root:

```
# .kustomizeignore
*.md
*.txt
tests/
.git/
```

Or use the `--exclude` flag (repeatable):

```bash
kustomize publish ghcr.io/org/repo:v1 --exclude '*.md' --exclude 'tests/*'
```

Kustomization files (`kustomization.yaml`, etc.) are never excluded.
The `.kustomizeignore` file itself is always excluded from the artifact.

## Example: Base + Overlay with OCI

### 1. Create a base kustomization

```bash
mkdir base && cd base
cat <<EOF > kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
- deployment.yaml
- service.yaml

commonLabels:
  app: myapp
EOF

cat <<EOF > deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myapp
spec:
  replicas: 1
  selector:
    matchLabels:
      app: myapp
  template:
    metadata:
      labels:
        app: myapp
    spec:
      containers:
      - name: myapp
        image: myapp:latest
        ports:
        - containerPort: 8080
EOF

cat <<EOF > service.yaml
apiVersion: v1
kind: Service
metadata:
  name: myapp
spec:
  ports:
  - port: 80
    targetPort: 8080
  selector:
    app: myapp
EOF
```

### 2. Publish the base

```bash
kustomize publish ghcr.io/myorg/myapp-base:v1.0.0 --path ./base
```

### 3. Create an overlay that references the OCI base

```bash
mkdir -p overlays/prod && cd overlays/prod
cat <<EOF > kustomization.yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
- oci://ghcr.io/myorg/myapp-base:v1.0.0

namespace: production

patches:
- patch: |
    apiVersion: apps/v1
    kind: Deployment
    metadata:
      name: myapp
    spec:
      replicas: 3
EOF
```

### 4. Build the overlay

```bash
kustomize build overlays/prod
```

This pulls `myapp-base:v1.0.0` from the registry and applies the production
overlay on top, producing a 3-replica deployment in the `production` namespace.
