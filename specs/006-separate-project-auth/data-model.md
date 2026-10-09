# Data Model: Separate Project Entry from Sign-In

## Sign-in choice

A declaration on one project.

| Field | Required | Rule |
| --- | --- | --- |
| `projectId` | Yes | Existing project identifier. One choice per project. |
| `signIn` | No | `shared` or `none`. Absent means `none`. |

Project PN is stored as `shared`. A new project with the field absent is `none`.

The choice does not contain business permissions. It does not name another project's door.

## Project door

The published addresses for one project. Each address binds to one internal entry.

| Field | Required | Rule |
| --- | --- | --- |
| `address` | Yes | Visitor host, and a path when the address is narrower than the whole host. |
| `service` | Yes | A Service in the same project namespace. |
| `port` | Yes | The Service port. It must already be allowed by that project's internal network from the platform ingress namespace. |
| `priority` | Yes | Higher priority wins. The narrower path is higher than the host-wide address. |

Project PN's door, unchanged as visitor roads:

| Address | Priority | Service | Port |
| --- | --- | --- | --- |
| Host `pn.example.com` and path prefix `/api` | 100 | `pn-backend` | 8080 |
| Host `pn.example.com` | 10 | `pn-frontend` | 80 |

A candidate door is rejected as a whole when any address fails the port rule, names `forward-auth`, or names a service outside the project. The last accepted door remains the published one.

## Internal network

The entries one project allows. The door reads this. The door does not write it.

| Field | Required | Rule |
| --- | --- | --- |
| `entryPort` | Yes | A TCP port the project accepts from the platform ingress namespace. |
| `insidePort` | No | A TCP port the project's own parts may call. Project PN allows 8080. |
| `signInEgress` | Only when `signIn` is `shared` | Egress to the gateway on port 8080. Forbidden when `signIn` is `none`. Not a visitor address. |

Project PN's allowed entry ports are 80 and 8080. Both door rows use those ports.

## Signed-in user

Present only after a `shared` project has asked and the sign-in service has confirmed the session.

| Field | Required | Rule |
| --- | --- | --- |
| `issuer` | Yes | The shared sign-in issuer. |
| `subject` | Yes | The stable subject from that issuer. |
| `audience` | Yes | This project only. |
| `expiresAt` | Yes | Confirmation is rejected at or after this time. |

Email is not an identifier. The pair `(issuer, subject)` does not grant private or administrative actions.

A `none` project has no signed-in user. A presented claim does not create one.

## State

```text
visitor opens project address
        |
        v
door serves the project
        |
        +-- signIn none --> unsigned-in, no sign-in call
        |
        +-- signIn shared, part does not need a known user --> unsigned-in is allowed
        |
        +-- signIn shared, part needs a known user
                |
                +-- confirmation succeeds --> signed-in for that project
                |
                +-- confirmation fails or sign-in is unavailable --> unsigned-in for that part
```

Sign-out ends the confirmed session. The next part that needs a known user is unsigned-in. Other projects' doors do not change.
