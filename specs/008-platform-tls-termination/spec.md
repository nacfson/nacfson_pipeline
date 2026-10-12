# Feature Specification: Platform-Owned TLS Termination

**Feature Branch**: `008-platform-tls-termination`

**Created**: 2026-10-12

**Status**: Draft

**Input**: User description: "The platform holds the TLS certificates only, and projects are routed through the ingress. Each project must not need its own internal TLS architecture." (Derived from the 2026-10-12 TLS debugging session: after PR #26, `auth.nacfson.cloud` received a trusted certificate but `pn.nacfson.cloud` did not, because certificate validation ran inside the project namespace and was rejected by the project's quota.)

## Context

Today, the public entry point serves a trusted certificate for the sign-in host, but not for Project PN. The previous change placed certificate objects, temporary validation workloads, a firewall exception and an admission exception **inside the project namespace**. The project quota (which correctly forbids node ports) rejected the validation workload, so Project PN still shows an untrusted certificate. Browsers warn, and native apps fail every API call.

This feature moves all TLS responsibility to the platform. A project only states "route this hostname to my services."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Visitors reach every project over trusted HTTPS (Priority: P1)

A visitor opens a published project (for example `https://pn.nacfson.cloud`) in a browser or a native app. The connection is trusted, with no warning, and every API call succeeds.

**Why this priority**: This is the current outage. Without it, Project PN is unusable from native apps and shows security warnings in browsers.

**Independent Test**: From a network outside the cluster, open each published hostname with a client that verifies certificates (browser, `curl` without insecure flags, the iOS app). Fully testable without any project change.

**Acceptance Scenarios**:

1. **Given** Project PN is published at `pn.nacfson.cloud`, **When** a visitor opens `https://pn.nacfson.cloud`, **Then** the client trusts the certificate and the page loads with no warning.
2. **Given** the native app calls `https://pn.nacfson.cloud/api/...`, **When** the request is sent, **Then** the TLS handshake succeeds and the API answers.
3. **Given** the sign-in host `auth.nacfson.cloud`, **When** a visitor is sent to the login page, **Then** the certificate is trusted, as it is today.
4. **Given** a visitor opens `http://pn.nacfson.cloud`, **When** the request arrives, **Then** the visitor is permanently redirected to the HTTPS address.

---

### User Story 2 - A project publishes a hostname with routing only (Priority: P2)

A project owner publishes or changes a public hostname. The project declares only the hostname, paths and target services. The project creates no certificate, runs no validation workload, opens no extra firewall rule and needs no quota headroom for TLS.

**Why this priority**: This is the structural fix. It prevents the same class of failure for Project PN and for every future project.

**Independent Test**: Inspect the project namespace and the project's declared configuration after deployment. They contain only routing and application objects. Certificate issuance and renewal for the project's hostname succeed while the project's quota and firewall rules stay unchanged.

**Acceptance Scenarios**:

1. **Given** a project that declares a route for its hostname, **When** the platform has a certificate covering that hostname, **Then** visitors get a trusted HTTPS connection with no TLS objects in the project namespace.
2. **Given** a project namespace that forbids node ports and has a tight quota, **When** the platform issues or renews that project's certificate, **Then** issuance succeeds and the project's quota usage does not change.
3. **Given** a project declares a hostname that the platform does not cover, **When** the change is validated before deployment, **Then** the platform rejects it with a clear message and does not deploy it.

---

### User Story 3 - The operator relies on automatic, observable certificate renewal (Priority: P3)

The operator does not take any manual step for certificates during normal operation. The operator can see each certificate's status, expiry and last renewal with read-only access, and gets a clear signal when issuance or renewal fails.

**Why this priority**: Trusted certificates expire. Without automatic, observable renewal, the outage in Story 1 comes back silently.

**Independent Test**: With read-only access, list all edge certificates and their expiry. Simulate a validation failure for one hostname and confirm that it is reported and that other hostnames keep working.

**Acceptance Scenarios**:

1. **Given** a certificate close to expiry, **When** the renewal window opens, **Then** the platform renews it without operator action and visitors see no interruption.
2. **Given** issuance fails for one hostname, **When** the operator checks status, **Then** the failure and its reason are visible, and all other hostnames keep serving trusted certificates.

---

### User Story 4 - Existing deployment migrates without extra downtime (Priority: P3)

The current deployment, which has certificate artifacts inside the project namespace, moves to the platform-owned model. The sign-in host keeps its trusted certificate the whole time, and Project PN gains a trusted certificate.

**Why this priority**: The cluster is live. The change must not break the host that already works.

**Independent Test**: Measure both hostnames from outside the cluster before, during and after the rollout.

**Acceptance Scenarios**:

1. **Given** the current state (sign-in host trusted, Project PN untrusted), **When** the migration is applied, **Then** the sign-in host never loses its trusted certificate, and Project PN serves a trusted certificate at the end.
2. **Given** the migration is complete, **When** the project namespace is inspected, **Then** it contains no certificate, TLS key material, validation workload, TLS firewall exception or TLS admission exception.

---

### Edge Cases

- A project route names a hostname with no matching platform certificate: the change must be rejected before deployment (Story 2, scenario 3). If such a route reaches the cluster anyway, visitors must get a failed or untrusted connection, never another project's certificate.
- Issuance fails for one hostname (DNS wrong, port closed, public CA unavailable, rate limit): other hostnames are unaffected, and the failure is visible to the operator.
- Renewal keeps failing as expiry approaches: the operator gets a signal well before expiry (see SC-005), not at expiry.
- A local or offline environment has no public DNS: it uses an environment-specific, untrusted certificate source, with no change to project configuration.
- A visitor connects with no hostname indication (raw IP): the platform answers with a non-project default and no project data.
- Several projects share the public entry point: certificate selection follows the requested hostname, and one project's change cannot replace another project's certificate.
- Private key exposure: the platform can replace a certificate and key without any project change.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The platform MUST terminate TLS for every public hostname at the single public entry point.
- **FR-002**: Edge certificates and their private keys MUST be held only in platform-owned locations. They MUST NOT exist in any project namespace or project configuration.
- **FR-003**: A project MUST be able to publish a hostname by declaring only the hostname, paths, target services and the intent to serve HTTPS. It MUST NOT need certificate objects, certificate validation workloads, TLS firewall exceptions, TLS admission exceptions or quota headroom for TLS.
- **FR-004**: Workloads that prove domain ownership to the certificate authority MUST run only in platform-owned space and MUST NOT consume project quota.
- **FR-005**: The platform MUST select the certificate to present based on the hostname the client requests.
- **FR-006**: The platform MUST issue certificates automatically and renew them before expiry, at the latest when 30 days of validity remain, without operator action.
- **FR-007**: Plain HTTP requests to public hostnames MUST be permanently redirected to HTTPS. Domain-ownership validation requests are the only exception.
- **FR-008**: The sign-in host MUST be covered by the same platform-owned mechanism as project hostnames.
- **FR-009**: Only the platform decides which hostnames receive certificates, through the platform's reviewed change process. A project MUST NOT be able to obtain a certificate for a hostname by itself. Coverage model: one certificate per published hostname, kept in a platform-owned list. A wildcard certificate for the shared parent domain is out of scope for this feature because it would add a DNS-provider credential.
- **FR-010**: Pre-deployment validation MUST reject a project route whose hostname is not covered by a platform certificate.
- **FR-011**: Traffic behind the public entry point (entry point to project services, and project to project or platform services) MUST remain unchanged plain HTTP on the cluster network. Projects MUST NOT be asked to change application code or configuration for TLS.
- **FR-012**: Environment differences in certificate source (publicly trusted on the VPS, self-issued locally) MUST be expressed only through environment-specific platform configuration (Constitution Principle VI).
- **FR-013**: The migration MUST remove the TLS artifacts now in the Project PN namespace: certificate, key material, and the validation firewall exception, and MUST narrow the certificate-manager admission exception to platform namespaces.
- **FR-014**: Edge private keys may be stored in the cluster's native secret storage inside platform namespaces, readable only by platform components (the ingress controller, the certificate manager, and the reconciler) and break-glass. They MUST NOT be delivered to project workloads. The Constitution's Principle V has been amended (Amendment 2.2.0) to state this exception explicitly.
- **FR-015**: With read-only access, the operator MUST be able to see each certificate's hostnames, status, expiry and last renewal.
- **FR-016**: Failure to issue or renew a certificate for one hostname MUST NOT affect the certificates served for other hostnames.
- **FR-017**: The Project PN workload MUST deploy the updated container images: backend `ghcr.io/nacfson/project-pn-backend@sha256:662c869b3c9e9559e518a844d8afc28b81844847081e00959e9b0f3c333a61a0` and frontend `ghcr.io/nacfson/project-pn-frontend@sha256:1da19ae7150980b4c0b50c7b8c7c207c2b41b13f00d32a429b901297776e70f6`.

### Key Entities

- **Public hostname**: An internet-facing name (for example `pn.nacfson.cloud`, `auth.nacfson.cloud`) that a project or the platform publishes. It is owned by exactly one project or by the platform.
- **Edge certificate**: The trusted credential the public entry point presents for one or more public hostnames. It has hostnames, an issuer, a validity period and a renewal status. It is owned by the platform.
- **Certificate inventory**: The platform-owned list that maps public hostnames to edge certificates. It is the only place where hostname coverage is decided.
- **Project route**: A project's declaration that a public hostname (and optional paths) is served by its services over HTTPS. It contains no certificate references.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: On the VPS environment, 100% of public hostnames present a publicly trusted certificate when checked from outside the cluster, with zero browser warnings.
- **SC-002**: A native-app smoke test against Project PN completes with zero TLS handshake failures.
- **SC-003**: Publishing or changing a project hostname requires zero TLS-related objects or exceptions in the project's namespace or configuration. Only routing changes are needed.
- **SC-004**: After the migration, the Project PN namespace contains zero certificate, key, validation-workload, TLS firewall or TLS admission artifacts, and certificate issuance changes the project's quota usage by zero.
- **SC-005**: Certificates renew automatically with at least 30 days of validity left, and a failing renewal becomes visible to the operator at least 14 days before expiry. No manual steps are needed across two consecutive renewal cycles.
- **SC-006**: 100% of plain HTTP requests to public hostnames receive a permanent redirect to HTTPS.
- **SC-007**: The sign-in host keeps a trusted certificate for 100% of the migration window, and Project PN serves a trusted certificate within 15 minutes after the change reaches the cluster.

## Assumptions

- Public hostnames resolve to the single public entry point (`168.110.101.69`), and ports 80 and 443 are reachable from the internet. Both were observed on 2026-10-12.
- A free, automated public certificate authority is used on the VPS environment. Domain ownership is proven over plain HTTP on port 80 for each hostname.
- The certificate-manager, issuers and redirect introduced by PR #26 are the baseline. This feature changes where certificates live and who owns them, not the tooling choice.
- The local environment accepts an untrusted, self-issued certificate.
- Encryption between services inside the cluster (mutual TLS or a service mesh) is out of scope.
- A CDN or proxy in front of the entry point is out of scope. DNS stays DNS-only.
- One public entry point serves all hostnames. Multiple entry points or high availability are out of scope for this feature.
