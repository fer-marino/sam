---
title: "Kubernetes Deployment and Local Testing Guide"
linkTitle: "Kubernetes Deployment and Local Testing Guide"
---
This guide explains how to deploy the SAM control plane and router in a Kubernetes cluster and how to test it locally with `kind` — using the bundled `make kind-*` targets for a one-command mesh, or a manual setup. Both paths need `cloud-provider-kind`.

> [!TIP]
> This guide focuses on local development sandboxing. For production-grade Kubernetes deployments (GKE, EKS, AKS), see the [Production Kubernetes Deployment](../../user/kubernetes-deployment/) guide.

---

## 1. Local Testing with Kind

The repository ships a one-command local mesh under `development/kind/`, driven by `make` targets. This is the fastest way to get a running control plane, router and console on your machine — ready for you to deploy services onto.

### Automated Mesh (Recommended)

```bash
make kind-up
```

This creates a `sam-kind` cluster (one control-plane plus two workers — one
labeled `sam-role: control-plane` for the router), builds the
`sam-control-plane:local`, `sam-router:local`, `sam-node:local` and
`sam-console:local` images, loads them into the cluster, and deploys:

- The **control plane**, configured to trust the cluster's own OIDC issuer.
- The **console**.
- **Dex**.
- The **router**.
- **No sam-nodes.** The mesh comes up empty; put services on it with the
  `charts/sam-node` chart (next section) or enroll a local node.

In-cluster nodes authenticate to the control plane via **Workload Identity Federation** (projected ServiceAccount tokens), so no static secrets or mock OIDC provider are needed.

The mesh is exposed through Gateway API LoadBalancer addresses, so there are no port-forwards and no `extraPortMappings`. `run.sh` runs **`cloud-provider-kind`** as a container (a hard prerequisite — it needs the docker socket) to serve those addresses, and prints them when the mesh is up:

- The **control plane** on its own address, routing the 8 exact enrollment paths, plus a dev-only `/admin` route.
- The **console** at `/console/` on that same address, shaped like the production deployment: `/console` 302s to `/console/`, and a `URLRewrite` filter strips the prefix before the console sees the request.
- **Dex** on its own address, deployed from `development/kind/dex.yaml` rather than the chart — Dex is an independent component the chart no longer bundles.
- The **router** at its own node's IP on port 4501, TCP **and** QUIC, announced from `status.hostIP`.

Once everything is up, `make kind-up` opens a tmux session with live per-pod logs (control plane and router, each in its own pane). Manage the mesh with:

```bash
make kind-up ARGS=-s     # bring the mesh up without attaching the log view
make kind-logs           # (re)attach the live-logs tmux session
make kind-down           # delete the sam-kind cluster and stop cloud-provider-kind
```

### Deploying a Service

A service is any backend a node advertises to the mesh (`type: mcp` or
`type: inference`). In Kubernetes a service ships as a **`charts/sam-node`
release**: one pod holding your service container and a `sam-node` sidecar
that advertises it. The repository ships ready-made examples under
`development/examples/`; each is a `Dockerfile` plus a `values.yaml`
describing only the service — kind-wide wiring (control plane URL, fast
discovery) lives once in `development/kind/sam-node.values.yaml` and is
stacked underneath with a second `-f`.

Deploy one (calc-mcp) into the running kind mesh:

```bash
docker build -t calc-mcp:local development/examples/calc-mcp
kind load docker-image --name sam-kind calc-mcp:local
helm --kube-context kind-sam-kind -n sam-kind install calc-mcp charts/sam-node \
  -f development/kind/sam-node.values.yaml \
  -f development/examples/calc-mcp/values.yaml
```

`development/deploy-kind-service.sh` wraps those commands (as
`helm upgrade --install`, plus a rollout wait) and echoes each one as it
runs, so deploying — or redeploying after a code change — is one line. It
takes a path to any directory holding a `Dockerfile` and a `values.yaml`,
and extra args pass through to helm:

```bash
./development/deploy-kind-service.sh development/examples/calc-mcp
./development/deploy-kind-service.sh development/examples/code-reviewer-pool/reviewer --set replicaCount=3
./development/deploy-kind-service.sh ~/src/my-service
# same service as a second, differently-labeled node:
./development/deploy-kind-service.sh development/examples/calc-mcp --release-name calc-b \
  --set-json 'extraArgs=["--discovery-interval=200ms"]' --set config.labels.region=us-east-1
```

To write your own service, copy an example folder: a backend listening on a
local port, a `Dockerfile`, and a `values.yaml` declaring the service —

```yaml
config:
  version: v1alpha1
  attenuation:
    policies: []
  services:
    - type: mcp
      name: my-service
      description: What it does
      target_url: http://127.0.0.1:7779/mcp
service:
  name: my-mcp
  image: my-mcp:local
```

The service container and `sam-node` share the pod's network, so `target_url`
is always `127.0.0.1:<port>`. Iterate with `docker build … && kind load … &&
helm upgrade calc-mcp charts/sam-node -f … -f …` — the chart rolls the pods
on config changes. Remove a service with `helm uninstall`.

Discover and call it from another node — enroll a local node and use the MCP
client:

```bash
make kind-local-node
# in another shell:
./bin/mcp-client -url http://127.0.0.1:9099/mcp -token devtoken -tool find_remote_tools -args '{}'
```

`find_remote_tools` lists the discovered tools (e.g. `mcp://my-service/...`)
and the peer hosting them; pass that `peer_id` and `tool_name` to
`call_remote_tool` to invoke it.

### Enrolling a Local Node

To iterate on `sam-node` without rebuilding the image, enroll a locally-built binary into the running mesh:

```bash
make build            # produce ./bin/sam-node
make kind-local-node
```

This mints a bootstrap token through the control plane's `/admin` API and runs `./bin/sam-node` against the control plane's gateway address — the same credential and path a real external node uses — exposing its MCP API on `127.0.0.1:9099` with the API token `devtoken`. Extra flags pass through via `ARGS`, e.g. `make kind-local-node
ARGS="--config my-node.yaml"` to host a service from a local config file
(same schema as the `config:` block in a `charts/sam-node` values file).

You can then drive it with the bundled MCP client:

```bash
./bin/mcp-client -url http://127.0.0.1:9099/mcp -token devtoken -tool find_remote_tools -args '{}'
```

### End-to-End Mesh Check

To verify the full discovery-and-call path against a freshly built mesh:

```bash
make kind-up ARGS="-s"
make kind-e2e-mesh
```

`kind-e2e-mesh` deploys `calc-mcp` as a `charts/sam-node` release, enrolls a local node, waits for it to discover `mcp://calculator/add`, calls `add(2, 3)`, and asserts the result is `5`.

---

## 2. Manual Deployment

If you'd rather deploy the pieces by hand — for example to exercise the Mock OIDC provider or wire up Google OIDC — you can apply the manifests below to a cluster yourself. SAM supports either a **Mock OIDC Provider** (recommended for quick local testing, since it needs no external credentials) or **Google OIDC** for authentication. The local `kind` path below uses `cloud-provider-kind` to allocate LoadBalancer IPs.

### Mock OIDC Provider Manifests (Optional)

The manifests for the mock OIDC provider are available in [mock-oidc.yaml](manifests/mock-oidc.yaml).

[mock-oidc.yaml](manifests/mock-oidc.yaml ':include')

### SAM Control Plane and Router Manifests

The manifests for the SAM Control Plane and Router are available in [sam-control-plane.yaml](manifests/sam-control-plane.yaml) and [sam-router.yaml](manifests/sam-router.yaml).

[sam-control-plane.yaml](manifests/sam-control-plane.yaml ':include')

[sam-router.yaml](manifests/sam-router.yaml ':include')

### Configuring Google OIDC (Optional)

To use Google as the OIDC provider instead of the mock provider:

2.  **No Redirect URI required:** Because `sam-node` implements RFC 8252 (dynamic loopback port selection for native apps), you don't need to configure a specific Redirect URI when setting up a Desktop app. The authorization server will automatically allow loopback redirects.
3.  **Update Secret:** Update the `sam-control-plane-secret` in `sam-control-plane.yaml` with your Google credentials:
    ```yaml
    SAM_OIDC_ISSUER: "https://accounts.google.com"
    SAM_OIDC_ID: "<your-client-id>.apps.googleusercontent.com"
    SAM_OIDC_SECRET: "<your-client-secret>"
    ```

### Deploying to Kind

#### Step 1: Create a Kind Cluster
```bash
kind create cluster --name sam-test
```

#### Step 2: Run cloud-provider-kind
Run it in a separate terminal:
```bash
cloud-provider-kind
```

#### Step 3: Load Images into Kind
```bash
kind load docker-image sam-control-plane:local --name sam-test
kind load docker-image sam-router:local --name sam-test
kind load docker-image sam-node:local --name sam-test
```

#### Step 4: Apply Manifests

If using the **Mock OIDC Provider**:
```bash
kubectl apply -f mock-oidc.yaml
kubectl apply -f sam-control-plane.yaml
kubectl apply -f sam-router.yaml
```

If using **Google OIDC**:
```bash
kubectl apply -f sam-control-plane.yaml
kubectl apply -f sam-router.yaml
```

#### Step 5: Get the External IP
You can use the following command to extract the allocated IP into an environment variable:

```bash
CONTROL_PLANE_IP=$(kubectl get svc sam-control-plane -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
```

---

## 3. Connecting an Agent

To connect a `sam-node` to the control plane, you just need its external IP and port.

### Enrolling the Agent

To connect a `sam-node` to the control plane for the first time, you need to enroll it. The node needs to authenticate with the control plane using a JWT token.

If you are using the **Mock OIDC Provider**, the node can fetch the token using OIDC Client Credentials flow:

1. **Get the Mock OIDC Service IP:**
   ```bash
   MOCK_IP=$(kubectl get svc mock-oidc -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
   ```

2. **Run the Node to enroll:**
   ```bash
   sam-node run \
     --control-plane "http://$CONTROL_PLANE_IP:9090" \
     --oidc-issuer "http://$MOCK_IP:18080" \
     --client-id "sam-mesh-audience" \
     # client secret via SAM_CLIENT_SECRET env or --client-secret-path
   ```

If you are using **Google OIDC**, you must obtain a valid Google ID token for your user and pass it via the `--jwt` flag:
```bash
sam-node run \
  --control-plane "http://$CONTROL_PLANE_IP:9090" \
  --jwt "<your-google-id-token>"
```

Once enrolled, the identity is stored in the local database (`agent.db`), and you can run subsequent times without OIDC credentials:
```bash
sam-node run
```

---

## 4. Automating Node Deployment

To automate the deployment of `sam-nodes` in Kubernetes and have them fetch the JWT token automatically, you can use a standard Kubernetes `Deployment` or `StatefulSet`.

### Example Deployment

Here is a sample manifest that uses the in-cluster DNS to fetch the token from the mock provider:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sam-node
spec:
  replicas: 3
  selector:
    matchLabels:
      app: sam-node
  template:
    metadata:
      labels:
        app: sam-node
    spec:
      containers:
      - name: sam-node
        image: sam-node:local
        command: ["sam-node", "run"]
        args:
        - "--control-plane"
        - "http://sam-control-plane:8080"
        - "--oidc-issuer"
        - "http://mock-oidc:18080"
        - "--client-id"
        - "sam-mesh-audience"
        - "--client-secret"
        - "sam-e2e-secret"
        env:
        - name: HOME
          value: /data
        volumeMounts:
        - name: data-volume
          mountPath: /data
      volumes:
      - name: data-volume
        emptyDir: {}
```

### Supported Authentication Flows

The SAM project supports three primary flows for acquiring a JWT token to enroll nodes, depending on the environment and security requirements:

#### 1. Client Credentials Flow (Machine-to-Machine)
*   **Description:** Defined in OAuth 2.0 RFC 6749, section 4.4. An application exchanges its application credentials (such as Client ID and Client Secret) for an access token.
*   **Use Case:** For unattended services or deployments connecting to a production OIDC provider.
*   **How to use:** Pass the `--oidc-issuer`, `--client-id`, and `--client-secret` flags to `sam-node run`.
*   **Example:**
```bash
sam-node run \
  --control-plane "http://control-plane.example.com:9090" \
  --oidc-issuer "https://accounts.google.com" \
  --client-id "$SAM_OIDC_ID" \
  --client-secret "$SAM_OIDC_SECRET"
```

#### 2. Native App Authorization Code Flow (Human Intervention)
*   **Description:** For devices operated by humans, this uses the standard Authorization Code Flow with PKCE for native apps (RFC 8252). The human operator runs `sam-node join` to open a web browser (or get a verification code via `--headless`), completes the login, and obtains a Biscuit token which is stored in the local database (`agent.db`).
*   **Use Case:** When a human operator is enrolling a node manually via their local terminal.
*   **How to use:** Run `sam-node join <control-plane-url>` before running the node daemon. Alternatively, you can obtain a token yourself and pass it via the `--jwt` flag to `sam-node run`.
*   **Example:**
```bash
# First, join interactively:
sam-node join https://control-plane.example.com

# Then start the node daemon:
sam-node run
```

#### 3. Workload Identity Federation (Secretless Kubernetes)
*   **Description:** The current best practice in Kubernetes. It removes the need for static secrets entirely. The machine proves its identity based on where it is running by presenting a ServiceAccount token (a signed JWT issued by the K8s API).
*   **Use Case:** Production Kubernetes deployments.
*   **How it works:** The Pod has a ServiceAccount token mounted. The Pod presents this token to the `sam-control-plane`. The control plane verifies it by calling back to the Kubernetes OIDC discovery endpoint.
*   **How to use:** Pass the path to the mounted ServiceAccount token to the `--jwt-path` flag.
*   **Example:**
```bash
sam-node run \
  --control-plane "http://control-plane.example.com:9090" \
  --jwt-path "/var/run/secrets/kubernetes.io/serviceaccount/token"
```
> [!NOTE]
> The `sam-control-plane` must be configured to trust the Kubernetes API server as an OIDC issuer for this flow to work.

---

## 5. Configuring Workload Identity in Kubernetes

Workload Identity allows `sam-node` pods to authenticate with the `sam-control-plane` using their Kubernetes ServiceAccount token, removing the need for static credentials.

Here are the exact steps to configure this:

### Step 1: Ensure OIDC Discovery is enabled on your Cluster
Most managed Kubernetes services (GKE, EKS, AKS) and local tools like `kind` support ServiceAccount Issuer Discovery.
In `kind`, this is enabled by default. You can find the issuer URL by running:
```bash
kubectl get --raw /.well-known/openid-configuration | jq -r .issuer
```
(Or check your cloud provider's documentation for the public issuer URL).

### Step 2: Configure the Control Plane to trust the Kubernetes Issuer
Update the `sam-control-plane` deployment to include the Kubernetes issuer URL in the `--issuer` flag.

If you are using `kind`, the issuer URL is usually `https://kubernetes.default.svc.cluster.local` (internal) or the external URL mapped by kind.

Update `sam-control-plane.yaml`:
```yaml
    spec:
      containers:
      - name: sam-control-plane
        args:
        - "--issuer"
        - "https://accounts.google.com,https://kubernetes.default.svc.cluster.local"
```

### Step 3: Create a ServiceAccount for the Node
Create a ServiceAccount that the `sam-node` pods will use.
```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: sam-node-sa
```

### Step 4: Deploy the Node with a Projected Volume
Deploy the `sam-node` and configure it to use the ServiceAccount. We use a **Projected Volume** to request a token with the specific audience expected by the control plane (e.g., the mesh name or a specific client ID).

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sam-node
spec:
  replicas: 3
  selector:
    matchLabels:
      app: sam-node
  template:
    metadata:
      labels:
        app: sam-node
    spec:
      serviceAccountName: sam-node-sa
      containers:
      - name: sam-node
        image: sam-node:local
        command: ["sam-node", "run"]
        args:
        - "--control-plane"
        - "http://sam-control-plane:8080"
        - "--jwt-path"
        - "/var/run/secrets/tokens/sam-token"
        volumeMounts:
        - name: sam-token
          mountPath: /var/run/secrets/tokens
          readOnly: true
      volumes:
      - name: sam-token
        projected:
          sources:
          - serviceAccountToken:
              path: sam-token
              expirationSeconds: 3600
              audience: "sam-control-plane-audience" # Match this with what the control plane expects
```
