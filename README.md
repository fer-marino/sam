# SAM: Sovereign Agent Mesh

<img alt="SAM" src="site/content/docs/sam_logo.png" />

SAM is a smart network built for autonomous AI agents:

*   **Zero Config:** Nodes discover each other and build the P2P network automatically.
*   **Zero Trust:** Every connection, node, and packet is strictly authenticated.
*   **Agentic Network:** Formed by lightweight nodes (`sam-node`) that provide self-healing, P2P connectivity, allowing autonomous agents to plug in, communicate, and invoke tools dynamically.
*   **Portability:** Cryptographic identities are environment-agnostic, allowing seamless node mobility across cloud, local, and edge environments.

Getting started is a one-liner (see the [Quick Start Guide](site/content/docs/quickstart.md)): install, add the skill, and your agent is on the mesh.

<img src="site/static/demo.gif" alt="Demo: installing SAM, adding the sam-mesh skill, and an agent discovering and calling tools across the mesh" width="100%" />

<details>
<summary><b>Advanced demo</b>: an agent fans a batch of work across a warm pool of reviewer agents on the mesh</summary>

<video src="https://github.com/user-attachments/assets/f1a61b6f-efcd-46d8-a6e6-659fb29dd1ce" width="100%" autoplay loop muted playsinline controls></video>

Full walkthrough: [Warm Agent Pool use case](site/content/docs/use-cases/warm-agent-pool.md).

</details>

---

## What "Sovereign" Means in SAM

SAM is an **open-source software project (Apache-2.0)** providing decentralized networking and cryptographic building blocks for autonomous AI agents. It carries no vendor telemetry and has no hardcoded dependencies on proprietary cloud services or model providers.

SAM provides the open protocols, cryptographic building blocks, and software to build **sovereign, zero-trust agent meshes**. In SAM, digital sovereignty is built on architectural and cryptographic control—custody of root keys, independent identity federation, and policy-enforced data boundaries. In a sovereign deployment, operators:

1. **Deploy Dedicated Mesh Infrastructure:** Run a dedicated control plane (`sam-control-plane`) and routing relays (`sam-router`) on your chosen infrastructure (managed cloud environments like Google Cloud, private Kubernetes clusters, or air-gapped datacenters) using our [Helm chart](charts/sam-mesh/README.md) or Kubernetes manifests.
2. **Maintain Root Cryptographic Key Custody:** Generate, manage, and hold your own Ed25519 root signing keys (via local HSMs, KMS, or Cloud EKM). You maintain 100% of the cryptographic authority—no external party can mint credentials, revoke nodes, or alter policies.
3. **Bring Your Own Identity Provider:** Bridge agent and user identities through your own OIDC identity provider (such as Dex, Keycloak, or corporate IdP).
4. **Enforce Territorial & Jurisdictional Boundaries:** Use cryptographically attested label gates (`labels: {jurisdiction: eu}` in the node config, `X-Sam-Required-Labels`) to mathematically guarantee prompts and tool invocations never leave authorized geographic scopes.
5. **Retain Autonomous Local Vetoes:** Configure local node attenuation policies (`sam-node.yaml`) to evaluate access rules *before* control plane grants, ensuring local nodes retain absolute veto authority.

> [!NOTE]
> **About the Public Developer Testnets:**
> The public endpoints (`bananas.sam-mesh.dev` and `hub.sam-mesh.dev`) are free testbeds created using community resources solely for developer testing, continuous integration, and rapid experimentation. They provide **no guarantees, no uptime commitments, zero SLA, and no sovereign guarantees**. Running on a shared community testnet delegates identity management to the testbed maintainers; true sovereignty requires deploying a dedicated control plane with customer-held keys.
>
> 📖 **Deep Dive:** Read our full **[Digital & Data Sovereignty Architecture](site/content/docs/sovereignty.md)** covering the 5 pillars, fail-closed label gates, uncooperative sandbox confinement, and regulatory alignment (GDPR Chapter V, EU Cloud Sovereignty Framework SEAL-3, EU Data Act).

---

## Architecture Components

*   `sam-control-plane`: The registry control plane for node identity registration, authorization policies, and router coordinating.
*   `sam-router`: The libp2p bootstrap nodes and relays providing data-plane connectivity and forwarding.
*   `sam-node`: The local node clients providing mesh transport integration and MCP sidecar routing.

---

## Documentation

Start exploring the Sovereign Agent Mesh:

### Digital & Data Sovereignty
- 🏛️ **[Digital & Data Sovereignty Architecture](site/content/docs/sovereignty.md)**: How SAM enforces data residency, territorial label gates, autonomous local vetoes, and regulatory compliance (GDPR, EU Cloud Sovereignty Framework SEAL-3, EU Data Act).

### For Users & Operators
- 🚀 **[User Quick Start Guide](site/content/docs/quickstart.md)**: Connect and run a SAM node on the community-hosted developer testnet (`bananas.sam-mesh.dev`, strictly for testing with no guarantees) using binaries or Docker.
- 🎛️ **[Dedicated Sovereign Deployment](site/content/docs/user/kubernetes-deployment.md)**: Run your own private sovereign hub (control plane, router, console) via the [sam-mesh Helm chart](charts/sam-mesh/README.md) or Kubernetes manifests.
- 🤖 **[Agent Integration Guides](site/content/docs/integrations/_index.md)**: Connect Google Gemini, Claude, and other AI agents to your SAM node to dynamically discover and call tools across the mesh.
- 📡 **[Testnet Validation Tutorial](site/content/docs/development/testnet-validation.md)**: Real-time verification, remote tool invocation, and HTTP stream proxies on public developer testnets.

### For Developers & Contributors
Compile from source, run local clusters, or execute tests:
- 🛠️ **[Developer Guide](site/content/docs/development/_index.md)**: Prereqs, compilation, local control plane setup, and Kubernetes Kind deployment.
- 🧪 **[Testing Guide](site/content/docs/development/testing.md)**: Go tests, E2E BATS, and containerized mesh execution.

---

## License

See [LICENSE](LICENSE).

## Disclaimer

This is not an officially supported Google product. This project is not eligible for the [Google Open Source Software Vulnerability Rewards Program](https://bughunters.google.com/open-source-security).
