package redact

import "testing"

func TestRedact(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"authorization bearer header", "Authorization: Bearer abc.def.ghi", "Authorization: [REDACTED]"},
		{"jwt in token param", "token=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig", "token=[REDACTED]"},
		{"google api key", "key AIzaSyA1234567890abcdefghijklmnopqrstuv", "key [REDACTED]"},
		{"sk key", "sk-proj-abcdefghijklmnopqrstuvwxyz", "[REDACTED]"},
		{"json password", `{"password":"hunter2","user":"a"}`, `{"password":"[REDACTED]","user":"a"}`},
		{"kv password", "password=hunter2 user=a", "password=[REDACTED] user=a"},
		{"no secrets", "no secrets here", "no secrets here"},
		{"json api_key with space", `{"api_key": "x"}`, `{"api_key": "[REDACTED]"}`},
		{"url query token", "GET /x?token=abc&user=a", "GET /x?token=[REDACTED]&user=a"},
		{"two secrets", "password=a1 secret=b2 ok=1", "password=[REDACTED] secret=[REDACTED] ok=1"},
		{"two secrets mixed", `{"token":"t1"} and sk-abcdefghijklmnopqrst`, `{"token":"[REDACTED]"} and [REDACTED]`},
		{"json non-string", `{"password":123,"user":"a"}`, `{"password":"[REDACTED]","user":"a"}`},
		{"json non-string end", `{"password":123}`, `{"password":"[REDACTED]"}`},
		{"case insensitive key", "PASSWORD=x Api_Key=y", "PASSWORD=[REDACTED] Api_Key=[REDACTED]"},
		{"containing key name", "db_password=x refresh_token=y", "db_password=[REDACTED] refresh_token=[REDACTED]"},
		{"header style api key", "X-Api-Key: abc123 def", "X-Api-Key: [REDACTED]"},
		{"header multiline", "Cookie: a=b\nuser: bob", "Cookie: [REDACTED]\nuser: bob"},
		{"kv semicolon and comma", "secret=a;user=b,token=c,x=1", "secret=[REDACTED];user=b,token=[REDACTED],x=1"},
		{"json escaped quote", `{"password":"a\"b","u":"v"}`, `{"password":"[REDACTED]","u":"v"}`},
		{"bare bearer", "got Bearer abc123 failed", "got [REDACTED] failed"},
		{"bare jwt", "bad eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig here", "bad [REDACTED] here"},
		{"quoted kv value with space", `password="a b" user=a`, `password=[REDACTED] user=a`},
		{"escaped quotes in nested json", `password=\"abc\" user=a`, `password=[REDACTED] user=a`},
		{"task- is not an sk key", "task-abcdefghijklmnopqrstuvwxyz", "task-abcdefghijklmnopqrstuvwxyz"},
		{"already redacted", `{"password":"[REDACTED]"}`, `{"password":"[REDACTED]"}`},
		// fix round 1: Bearer behind a key=value separator.
		{"authorization assign bearer", "authorization=Bearer abc.def", "authorization=[REDACTED]"},
		{"authorization assign BEARER", "Authorization=BEARER abc123", "Authorization=[REDACTED]"},
		{"json authorization bearer", `{"authorization":"Bearer abc","user":"a"}`, `{"authorization":"[REDACTED]","user":"a"}`},
		// fix round 1: array/object values.
		{"json cookie array", `{"Cookie":["sid=abc123"]}`, `{"Cookie":"[REDACTED]"`},
		{"json set-cookie array", `{"Set-Cookie":["sid=abc123; Path=/"]}`, `{"Set-Cookie":"[REDACTED]"`},
		{"json token object", `{"token":{"v":"abc123"}}`, `{"token":"[REDACTED]"`},
		{"json credentials object", `{"credentials":{"user":"a","pass":"x"}}`, `{"credentials":"[REDACTED]"`},
		{"json nested array", `{"token":[["abc123"],["d]ef"]],"u":"v"}`, `{"token":"[REDACTED]"`},
		{"passphrase", `{"passphrase":"abc123"}`, `{"passphrase":"[REDACTED]"}`},
		{"client_secret json", `{"client_secret":"abc123"}`, `{"client_secret":"[REDACTED]"}`},
		// fix round 1: python repr, spaces around =, truncated JSON.
		{"python repr", `{'password': 'hunter2', 'user': 'a'}`, `{'password': "[REDACTED]", 'user': 'a'}`},
		{"python repr double value", `{'token': "abc 123", 'user': 'a'}`, `{'token': "[REDACTED]", 'user': 'a'}`},
		{"spaces around equals", "password = hunter2 user=a", "password = [REDACTED] user=a"},
		{"client_secret single quoted", "client_secret = 'abc def' ok=1", "client_secret = [REDACTED] ok=1"},
		{"ruby hashrocket", `"password" => "hunter2"`, `"password" => "[REDACTED]"`},
		{"truncated json", `{"password":"abc`, `{"password":"[REDACTED]"`},
		{"truncated json escaped tail", `{"user":"a","token":"abc\`, `{"user":"a","token":"[REDACTED]"`},
		{"truncated single quote", `{'password': 'abc`, `{'password': "[REDACTED]"`},
		{"truncated assign quote", `password="abc def`, `password=[REDACTED]`},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := String(tt.in); got != tt.want {
				t.Errorf("String(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
