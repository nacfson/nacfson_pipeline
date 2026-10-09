# Contract: Session return

Used only when a `shared` project asks who the user is. The auth host allowlist is unchanged: sign-in pages, the callback, and sign-out. Project hosts are not added to it.

## Start

The project sends the browser to the existing sign-in start on the auth host.

| Input | Rule |
| --- | --- |
| `return` | An address already on that project's door. Any other return is rejected. |
| project | The project whose `signIn` is `shared`. A `none` project has no start. |

## Return

After a confirmed sign-in, the browser is sent to the same `return` address. The response sets a cookie:

| Property | Value |
| --- | --- |
| Scope | The project host only |
| `HttpOnly` | Yes |
| `Secure` | Yes |
| Contents | The confirmed session for that project |

The cookie is not required to open the project host.

## Confirmation

A `shared` project may ask the gateway, over the cluster network, whether that session is still confirmed.

| Result | Project behavior |
| --- | --- |
| Confirmed | Accept the identity only when the signature, issuer, audience, and expiry fit this project. |
| Rejected, expired, or signed for another project | The visitor is unsigned-in for the part that asked. |
| Sign-in service unreachable | The visitor is unsigned-in for the part that asked. Other parts of the project still answer. |

A `none` project does not call confirmation. Sign-out still ends only the session that was confirmed, through the existing authenticated sign-out page.
