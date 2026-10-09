# Feature Specification: Separate Project Entry from Sign-In

**Feature Branch**: `006-separate-project-auth`

**Created**: 2026-10-09

**Status**: Draft

**Input**: User description: "The shared sign-in service provides authentication only. A project is reached by its own door, and that door follows the project's internal network. A project may use a sign-in session after it is reached, or it may not use sign-in at all. The visitor's original road to the project stays the same."

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Open a project that does not use sign-in (Priority: P1)

A visitor opens a project that has chosen not to use the shared sign-in service. The visitor reaches that project and can complete its public task. The sign-in service is not involved.

**Why this priority**: This is the change from today's behavior. A project must be usable without the sign-in service standing in the road.

**Independent Test**: Stop the shared sign-in service. Open the project that does not use sign-in. The visitor receives the project and completes its public task.

**Acceptance Scenarios**:

1. **Given** a project that does not use shared sign-in, **When** a visitor opens that project's published address, **Then** the visitor reaches the project without a sign-in session.
2. **Given** the shared sign-in service is unavailable, **When** a visitor opens a project that does not use sign-in, **Then** the project still answers.
3. **Given** a visitor presents a claim of identity to a project that does not use sign-in, **When** the project answers, **Then** the project does not treat that claim as a signed-in user.

---

### User Story 2 - Open a project, then sign in only if it asks (Priority: P1)

A visitor opens a project that does use shared sign-in. The visitor reaches the project on that project's own door. The project asks the visitor to sign in only for the parts that need a known user. Signing in answers who the user is. It does not choose a different road into the project.

**Why this priority**: Projects that want a known user still need sign-in, and that sign-in must happen after the project is reached, on the same road.

**Independent Test**: Open a project that uses sign-in. Confirm the project appears before any sign-in request. Complete sign-in when the project asks. Confirm the visitor returns to the same project address.

**Acceptance Scenarios**:

1. **Given** a project that uses shared sign-in, **When** a visitor opens that project's published address, **Then** the visitor reaches the project before any sign-in request.
2. **Given** the visitor has reached that project, **When** the visitor opens a part that needs a known user, **Then** the project asks the shared sign-in service who the user is and returns the visitor to the same project.
3. **Given** the shared sign-in service is unavailable, **When** a visitor opens a part of that project that does not need a known user, **Then** that part still answers.
4. **Given** the shared sign-in service is unavailable, **When** a visitor opens a part that needs a known user, **Then** the project does not treat the visitor as signed in.

---

### User Story 3 - A project's door matches its own network (Priority: P2)

An operator publishes a project. The project's door admits visitors only through entries that the project's own internal network already allows. The sign-in service does not add, remove, or replace those entries.

**Why this priority**: The door and the internal network are one project's settings. They must agree, and they must stay independent of sign-in.

**Independent Test**: Publish a project whose door names only entries its internal network allows, and confirm visitors can use those entries. Publish a candidate whose door names an entry the internal network does not allow, and confirm the candidate is rejected.

**Acceptance Scenarios**:

1. **Given** a project's internal network allows a site entry and an API entry, **When** the project's door is published, **Then** the site address uses the site entry and the API address uses the API entry.
2. **Given** a candidate door names an entry the project's internal network does not allow, **When** the candidate is reviewed, **Then** the candidate is rejected and the existing door stays unchanged.
3. **Given** two projects with opposite sign-in choices, **When** either project is published or visited, **Then** neither project's door or internal network changes because of the other project's sign-in choice.

---

### Edge Cases

- What happens when the shared sign-in service is unavailable and the project does not use sign-in? The project still answers on its published addresses.
- What happens when the shared sign-in service is unavailable and the project uses sign-in? Parts that do not need a known user still answer. Parts that need a known user do not treat the visitor as signed in.
- What happens when a project door names an entry the project's internal network does not allow? The door is rejected before visitors are sent there.
- What happens when a visitor presents a forged identity to a project that uses sign-in? The project rejects that identity. A valid identity for a different project is also rejected.
- What happens when one project uses sign-in and another does not? Each visitor road stays with its own project. The project that does not use sign-in has no connection to the sign-in service.
- What happens to the existing first project's site address and API address? Both remain the visitor's roads into that project. Sign-in does not replace either road.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Every project MUST declare whether it uses the shared sign-in service. A new project that omits the declaration MUST be treated as not using shared sign-in.
- **FR-002**: A visitor MUST be able to reach a project that does not use shared sign-in, without a sign-in session and without a connection to the sign-in service.
- **FR-003**: When the shared sign-in service is unavailable, a project that does not use it MUST continue to answer on its published addresses.
- **FR-004**: A visitor MUST reach a project that uses shared sign-in by that project's own door, before the project asks the visitor to sign in.
- **FR-005**: The shared sign-in service MUST only establish who the user is. It MUST leave the project's published addresses and internal entries unchanged.
- **FR-006**: After a successful sign-in, the visitor MUST return to the same project address the visitor was already using.
- **FR-007**: Every published address on a project's door MUST use an entry that the project's internal network allows. A door that names any other entry MUST be rejected, and the previously accepted door MUST stay in place.
- **FR-008**: A project that does not use shared sign-in MUST NOT be required to connect to the sign-in service, and MUST NOT treat a presented identity claim as a signed-in user.
- **FR-009**: When a project that uses shared sign-in cannot confirm who the user is, it MUST NOT treat the visitor as signed in. Addresses of that project that do not need a known user MUST still answer.
- **FR-010**: One project's sign-in choice MUST NOT change another project's door or internal network.
- **FR-011**: The existing first project's site address and API address MUST remain the visitor's roads into that project. Sign-in MUST NOT replace either road with a different one.
- **FR-012**: A project that uses shared sign-in MUST accept a confirmed identity only when it is genuine, unexpired, and meant for that project. An identity meant for another project MUST be rejected. A confirmed identity MUST NOT by itself grant the project's private or administrative actions.

### Key Entities

- **Sign-in choice**: A project's declaration that it uses the shared sign-in service, or that it does not. A missing declaration means it does not.
- **Project door**: The published addresses a visitor uses to reach one project. Each address is tied to one entry the project's internal network allows.
- **Internal network**: The entries a project allows, including which outside entry can reach the project and which calls its own parts may make to each other.
- **Signed-in user**: The identity established by the shared sign-in service for one project, after that project has asked. It names who the user is and does not name the user's business permissions.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In 10 consecutive trials with the shared sign-in service unavailable, a visitor opens a project that does not use sign-in and receives that project every time.
- **SC-002**: In 10 consecutive trials, a visitor opens a project that uses sign-in and sees the project before any sign-in request, every time.
- **SC-003**: In every reviewed project, each published address matches an entry the project's internal network allows. Every candidate that breaks that match is rejected, and the previous door remains usable.
- **SC-004**: A visitor completes the public task of a project that does not use sign-in on the first attempt, with no sign-in step, in all trials of that project.
- **SC-005**: After signing in, the visitor is back on the same project address in all trials. No trial lands the visitor on a different project's address.

## Assumptions

- The platform already has one shared sign-in service. This feature keeps that service as the only shared way to establish who a user is. It does not add a second sign-in service and does not change how a person proves a Google account when a project asks for sign-in.
- The existing first project declares that it uses shared sign-in. Visitors reach it first. It asks for sign-in only for the parts that need a known user. This feature does not turn sign-in off for that project.
- The existing first project already has two visitor roads, a site address and an API address, and its internal network already allows the entries those roads use. This feature keeps that match.
- A new project defaults to not using shared sign-in until its declaration says that it does.
- Business permissions inside a project stay that project's own rules. Establishing who the user is does not grant private data or administration.
- Access used by machines and stored secrets stays separate from visitor sign-in. This feature does not change that access.
- The current platform constitution requires a sign-in check before a project may answer. Deployment of this feature depends on a constitution amendment that records this specification. This specification does not itself amend the constitution.
- The public pages used to start sign-in, finish sign-in, and sign out remain available to visitors who are not yet signed in. Administrative sign-in controls remain private.
