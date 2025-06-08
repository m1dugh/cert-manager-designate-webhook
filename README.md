# Cert Manager Designate Webhook

A webhook for cert-manager to connect to designate dns server.

## How to use ?

### Installing Credentials

The credentials for the designate server must be added in a secret prior
to chart installation.

Here is an example for the secret

```bash
kubectl create secret generic openstack-custom-creds \
    --namespace cert-manager \
    --from-literal OS_AUTH_TYPE=v3applicationcredential \
    --from-literal OS_AUTH_URL=<auth_url> \
    --from-literal OS_IDENTITY_API_VERSION=3 \
    --from-literal OS_REGION_NAME=<region> \
    --from-literal OS_INTERFACE=<iface> \
    --from-literal OS_APPLICATION_CREDENTIAL_ID=<cred_id> \
    --from-literal OS_APPLICATION_CREDENTIAL_SECRET=<secret>
```

These credentials come from any valid openstack openrc file

### Installing chart

The helm chart can then be installed in the same namespace

```bash
helm upgrade --install \
    --namespace cert-manager \
    ./deploy/chart \
    --set existingSecretName openstack-custom-creds \
    cert-manager-designate-webhook
```

### Using with issuer

You can then deploy a cluster issuer or an issuer with the following manifest.

Don't forget to replace `<your-designate-zone-id>` and `<secret-key-ref>` with
proper values.

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-staging-issuer
spec:
  acme:
    privateKeySecretRef:
      name: <secret-key-ref>
    server: https://acme-staging-v02.api.letsencrypt.org/directory
    solvers:
    - dns01:
        webhook:
          config:
            zone_id: <your-designate-zone-id>
          groupName: acme.midugh.fr
          solverName: designate-solver
```
