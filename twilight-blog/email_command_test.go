package main

import "testing"

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{
			name: "short flag separate value",
			args: []string{"-target", "abc@def.com"},
			want: "abc@def.com",
		},
		{
			name: "long flag separate value",
			args: []string{"--target", "abc@def.com"},
			want: "abc@def.com",
		},
		{
			name: "short flag equals value",
			args: []string{"-target=abc@def.com"},
			want: "abc@def.com",
		},
		{
			name: "long flag equals value",
			args: []string{"--target=abc@def.com"},
			want: "abc@def.com",
		},
		{
			name:    "missing value",
			args:    []string{"-target"},
			wantErr: true,
		},
		{
			name:    "missing target",
			args:    []string{},
			wantErr: true,
		},
		{
			name:    "empty target",
			args:    []string{"-target", "   "},
			wantErr: true,
		},
		{
			name:    "invalid email",
			args:    []string{"-target", "not-an-email"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTarget(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTarget(%v) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseTarget(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
