# garage-s3-operator

This chart installs the Garage Operator runtime resources.

It intentionally excludes CustomResourceDefinitions, which are shipped separately in the `garage-s3-operator-crds` chart.

## Syncing generated RBAC

Run:

```bash
make helm-operator-sync
```

This updates the generated manager `ClusterRole` template from `deploy/kustomize/rbac/role.yaml`.
