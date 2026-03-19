package ocrlang

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "english alias", input: "en", want: "eng"},
		{name: "russian alias", input: "ru", want: "rus"},
		{name: "tajik alias tg", input: "tg", want: "tgk"},
		{name: "tajik alias tj", input: "tj", want: "tgk"},
		{name: "tajik alias tjk", input: "tjk", want: "tgk"},
		{name: "multi language", input: "tj+ru", want: "tgk+rus"},
		{name: "locale alias", input: "en-US", want: "eng"},
		{name: "dedupe canonical", input: "en+eng", want: "eng"},
		{name: "preserve canonical", input: "deu", want: "deu"},
		{name: "invalid short", input: "zz", wantErr: true},
		{name: "invalid token", input: "en+12", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Normalize(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	got, err := Resolve("", "tj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tgk" {
		t.Fatalf("expected tgk, got %q", got)
	}

	got, err = Resolve("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "eng" {
		t.Fatalf("expected eng, got %q", got)
	}
}
