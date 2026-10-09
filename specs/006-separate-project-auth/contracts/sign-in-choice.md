# Contract: Sign-in choice

## Declaration

```yaml
signIn: shared   # or none
```

| Value | Meaning |
| --- | --- |
| omitted | `none` |
| `none` | The project never connects to the sign-in service. A presented identity is not a signed-in user. |
| `shared` | The project may ask who the user is after the visitor has arrived. |

Project PN declares `shared`. A realm client is provisioned only for `shared`. Changing a project to `none` does not delete another project's client, door, or network.

## What the sign-in service does not do

It does not add a route, remove a route, or change a port on the project door. It does not change the project's allowed entry ports.
