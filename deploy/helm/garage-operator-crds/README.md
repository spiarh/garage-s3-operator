# garage-s3-operator-crds

This chart installs the Garage Operator CustomResourceDefinitions.

The CRD manifests live in `templates/` instead of Helm's `crds/` directory so Helm upgrades can apply CRD updates as part of regular chart upgrades.

## Syncing CRDs

Run:

```bash
make helm-crds-sync
```

This copies the CRD manifests from `deploy/kustomize/crd/bases` into this chart's `templates/` directory.
