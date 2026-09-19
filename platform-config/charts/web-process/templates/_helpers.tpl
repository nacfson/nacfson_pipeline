{{/*
Expand chart name.
*/}}
{{- define "web-process.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "web-process.fullname" -}}
{{- .Values.project | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Common labels applied to every resource.
*/}}
{{- define "web-process.labels" -}}
app.kubernetes.io/name: {{ include "web-process.name" . }}
app.kubernetes.io/instance: {{ .Values.project }}
app.kubernetes.io/component: application
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
platform.processmanager.dev/project: {{ .Values.project }}
platform.processmanager.dev/access-policy: {{ .Values.accessPolicy }}
platform.processmanager.dev/platform: {{ .Values.platform | replace "/" "-" }}
{{- if .Release.Labels }}
{{- with index .Release.Labels "platform.processmanager.dev/git-revision" }}
platform.processmanager.dev/git-revision: {{ . | quote }}
{{- end }}
{{- end }}
{{- end -}}

{{/*
Selector labels for application pods.
*/}}
{{- define "web-process.selectorLabels" -}}
app.kubernetes.io/name: {{ include "web-process.name" . }}
app.kubernetes.io/instance: {{ .Values.project }}
app.kubernetes.io/component: application
platform.processmanager.dev/project: {{ .Values.project }}
{{- end -}}

{{/*
Selector labels for oauth2-proxy pods.
*/}}
{{- define "web-process.proxySelectorLabels" -}}
app.kubernetes.io/name: {{ include "web-process.name" . }}
app.kubernetes.io/instance: {{ .Values.project }}
app.kubernetes.io/component: oauth2-proxy
platform.processmanager.dev/project: {{ .Values.project }}
{{- end -}}

{{/*
Service account name.
*/}}
{{- define "web-process.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "web-process.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Immutable image reference by digest.
*/}}
{{- define "web-process.image" -}}
{{- if .Values.image.tag -}}
{{- fail "image.tag is forbidden; supply image.digest only" -}}
{{- end -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- end -}}

{{/*
Pinned oauth2-proxy image.
*/}}
{{- define "web-process.proxyImage" -}}
{{- if .Values.oauth2Proxy.image.tag -}}
{{- fail "oauth2Proxy.image.tag is forbidden; supply digest only" -}}
{{- end -}}
{{- printf "%s@%s" .Values.oauth2Proxy.image.repository .Values.oauth2Proxy.image.digest -}}
{{- end -}}

{{/*
Validate access-policy specific fields at render time.
*/}}
{{- define "web-process.validateAccessPolicy" -}}
{{- if not (has .Values.accessPolicy (list "public" "oidc-protected" "oidc-native")) -}}
{{- fail (printf "unsupported accessPolicy %q" .Values.accessPolicy) -}}
{{- end -}}
{{- if eq .Values.accessPolicy "public" -}}
{{- if .Values.oidc -}}
{{- if or .Values.oidc.clientId .Values.oidc.secretName .Values.oidc.allowedGroup .Values.oidc.callbackURIs .Values.oidc.issuerURL -}}
{{- fail "public accessPolicy must not set oidc fields" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if eq .Values.accessPolicy "oidc-protected" -}}
{{- if or (not .Values.oidc) (not .Values.oidc.clientId) (not .Values.oidc.secretName) (not .Values.oidc.allowedGroup) (not .Values.oidc.callbackURIs) (not .Values.oidc.issuerURL) (not .Values.hostname) -}}
{{- fail "oidc-protected requires hostname and oidc.clientId, secretName, allowedGroup, callbackURIs, issuerURL" -}}
{{- end -}}
{{- if ne .Values.oidc.secretName (printf "%s-oidc" .Values.project) -}}
{{- fail "oidc.secretName must be the project-owned Secret <project>-oidc; shared or cross-project Secrets are forbidden" -}}
{{- end -}}
{{- range .Values.oidc.callbackURIs -}}
{{- if or (contains "*" .) (not (hasPrefix "https://" .)) -}}
{{- fail (printf "callback URI must be exact HTTPS without wildcards: %s" .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if eq .Values.accessPolicy "oidc-native" -}}
{{- if or (not .Values.oidc) (not .Values.oidc.clientId) (not .Values.oidc.callbackURIs) (not .Values.oidc.issuerURL) (not .Values.hostname) -}}
{{- fail "oidc-native requires hostname and oidc.clientId, callbackURIs, issuerURL" -}}
{{- end -}}
{{- if or .Values.oidc.secretName .Values.oidc.allowedGroup -}}
{{- fail "oidc-native must not set oauth2-proxy secretName or allowedGroup" -}}
{{- end -}}
{{- range .Values.oidc.callbackURIs -}}
{{- if or (contains "*" .) (not (hasPrefix "https://" .)) -}}
{{- fail (printf "callback URI must be exact HTTPS without wildcards: %s" .) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- with .Values.podAnnotations -}}
{{- range $k, $v := . -}}
{{- if or (hasPrefix "X-Platform-" $k) (hasPrefix "X-Forwarded-" $k) (eq $k "X-Auth-Request-Email") -}}
{{- fail (printf "reserved identity header annotation forbidden: %s" $k) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
