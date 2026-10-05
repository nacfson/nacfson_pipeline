# Spec Kit (Spec-Driven Development) Guidelines

When using Spec Kit in this project, follow the Spec-Driven Development (SDD) workflow:

1. **Constitution** (`/speckit-constitution`): Define core engineering principles, non-negotiable standards, and governance.
2. **Specify** (`/speckit-specify`): Define requirements, user scenarios, and acceptance criteria without jumping into implementation details.
3. **Clarify** (`/speckit-clarify`): Resolve ambiguities before planning.
4. **Plan** (`/speckit-plan`): Establish technical architecture, data model, and implementation approach.
5. **Tasks** (`/speckit-tasks`): Break the plan into granular, dependency-ordered tasks.
6. **Implement** (`/speckit-implement`): Execute tasks with automated verification.

## Planning & Artifact Standards
When generating implementation plans (via `/plan` or `/speckit-plan`):
- Always generate a self-contained, modern HTML version of the plan within the repository at: `agy_plan/html/<plan-name>.html`.
- Include visual architecture diagrams (SVG/Mermaid), scope breakdown, code diffs, and automated verification steps in the HTML plan.
- Provide a direct clickable link to the generated HTML plan file in the response.
