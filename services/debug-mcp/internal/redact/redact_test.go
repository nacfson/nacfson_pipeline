package redact

import (
	"reflect"
	"strings"
	"testing"
)

func TestRedactPatterns(t *testing.T) {
	input := strings.Join([]string{
		`Authorization: Bearer supersecrettoken123`,
		`upstream error: Bearer standalonebearer789`,
		`token=rawtokenvalue456`,
		`{"api_key":"secretvaluejson","ok":"public"}`,
		`jwt eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signaturepart`,
		`gcp AIzaSyDummyKeyValue12345678901234567890`,
		`openai sk-proj-abcdefghijklmnop123456`,
	}, "\n")

	got, kinds := Redact(input)

	for _, forbidden := range []string{
		"supersecrettoken123",
		"standalonebearer789",
		"rawtokenvalue456",
		"secretvaluejson",
		"eyJhbGciOi",
		"AIzaSyDummy",
		"sk-proj-abc",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("redacted output still leaked %q:\n%s", forbidden, got)
		}
	}

	wantKinds := []string{KindBearer, KindGoogleKey, KindJWT, KindSecretField, KindSKKey}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", kinds, wantKinds)
	}

	gotAgain, kindsAgain := Redact(got)
	if gotAgain != got || len(kindsAgain) != 0 {
		t.Fatalf("second pass changed redacted text or flagged kinds=%v", kindsAgain)
	}
}

func TestStripEnvAndEnvFrom(t *testing.T) {
	envs := StripEnv([]EnvVarInput{
		{Name: "DB_PASSWORD", Value: "hunter2"},
		{
			Name: "OAUTH_SECRET",
			ValueFrom: &EnvVarSourceInput{
				SecretKeyRef: &ObjectKeySelector{Name: "auth-secrets", Key: "client_secret"},
			},
		},
		{
			Name: "APP_MODE",
			ValueFrom: &EnvVarSourceInput{
				ConfigMapKeyRef: &ObjectKeySelector{Name: "app-cfg", Key: "mode"},
			},
		},
		{
			Name: "POD_IP",
			ValueFrom: &EnvVarSourceInput{
				FieldRef: &FieldSelector{FieldPath: "status.podIP"},
			},
		},
	})

	want := []RedactedEnvVar{
		{Name: "DB_PASSWORD", Source: "literal"},
		{Name: "OAUTH_SECRET", Source: "secretKeyRef"},
		{Name: "APP_MODE", Source: "configMapKeyRef"},
		{Name: "POD_IP", Source: "fieldRef"},
	}
	if !reflect.DeepEqual(envs, want) {
		t.Fatalf("StripEnv = %+v, want %+v", envs, want)
	}

	from := StripEnvFrom([]EnvFromInput{
		{ConfigMapRef: &NamedObjectRef{Name: "cm-a"}},
		{SecretRef: &NamedObjectRef{Name: "sec-b"}},
	})
	wantFrom := []string{"configMapRef", "secretRef"}
	if !reflect.DeepEqual(from, wantFrom) {
		t.Fatalf("StripEnvFrom = %v, want %v", from, wantFrom)
	}
}
