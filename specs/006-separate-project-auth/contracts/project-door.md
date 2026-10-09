# Contract: Project door

The visitor road into a project. It is not a sign-in check.

## Published addresses

A project door is a set of routes. Each route names one Service in that project's namespace and one port.

Project PN:

| Match | Priority | Middleware | Service |
| --- | --- | --- | --- |
| Host `pn.example.com` and path prefix `/api` | 100 | `strip-forged-headers` | `pn-backend:8080` |
| Host `pn.example.com` | 10 | `strip-forged-headers` | `pn-frontend:80` |

`forward-auth` is absent. No route on this host is added to the auth-host allowlist.

## Header strip

`strip-forged-headers` clears `X-User-Subject`, `X-User-Issuer`, and `Authorization` on every project route before the Service sees the request.

## Match to the internal network

For every route:

1. The Service exists in the project namespace.
2. The port is present on that Service.
3. The project's NetworkPolicy allows ingress on that port from the platform namespace.
4. The middleware list includes `strip-forged-headers` and does not include `forward-auth`.

Failure of any rule rejects the candidate door. The last accepted door stays published.

Project PN's NetworkPolicy already allows ports 80 and 8080 from the platform namespace, and it allows the project's own pods to call port 8080. Those rules stay. The door uses 80 and 8080 and does not add a port.

## Sign-in egress

When `signIn` is `shared`, the project's NetworkPolicy may allow egress to the gateway on port 8080. That rule is not a visitor route.

When `signIn` is `none`, egress to the gateway and to the sign-in service is absent.

NodePort and LoadBalancer remain rejected.
