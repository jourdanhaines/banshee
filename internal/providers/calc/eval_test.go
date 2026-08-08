package calc

import (
	"math"
	"testing"
)

func TestEval(t *testing.T) {
	tests := []struct {
		expr    string
		want    float64
		wantErr bool
	}{
		// precedence and associativity
		{expr: "2+3*4", want: 14},
		{expr: "10-2-3", want: 5},
		{expr: "20/2/5", want: 2},
		{expr: "2^3^2", want: 512}, // right-associative
		{expr: "-2^2", want: -4},   // unary binds above power
		{expr: "2^-2", want: 0.25},
		{expr: "(2+3)*4", want: 20},
		{expr: "10%3", want: 1},
		{expr: "--3", want: 3},
		{expr: "+5", want: 5},

		// floats and scientific notation
		{expr: "1.5*2", want: 3},
		{expr: ".5+.5", want: 1},
		{expr: "2e3+1", want: 2001},
		{expr: "1E-2", want: 0.01},

		// identifiers
		{expr: "pi", want: math.Pi},
		{expr: "e", want: math.E},
		{expr: "2*PI", want: 2 * math.Pi},
		{expr: "sqrt(16)", want: 4},
		{expr: "abs(-3)", want: 3},
		{expr: "floor(1.9)", want: 1},
		{expr: "ceil(1.1)", want: 2},
		{expr: "round(2.5)", want: 3},
		{expr: "sqrt(abs(-16))", want: 4},
		{expr: "sqrt(9)+1", want: 4},

		// whitespace
		{expr: " 1 + 2 ", want: 3},

		// non-finite is legal for Eval (the provider filters)
		{expr: "1/0", want: math.Inf(1)},

		// errors
		{expr: "", wantErr: true},
		{expr: "1.2.3", wantErr: true},
		{expr: "192.168.1.1", wantErr: true},
		{expr: "12:30", wantErr: true},
		{expr: "0x1A", wantErr: true},
		{expr: "2++", wantErr: true},
		{expr: "(1+2", wantErr: true},
		{expr: "1+2)", wantErr: true},
		{expr: "foo(1)", wantErr: true},
		{expr: "sqrt 4", wantErr: true},
		{expr: "1 2", wantErr: true},
		{expr: "vim 2", wantErr: true},
		{expr: "2e", wantErr: true},
		{expr: "hello", wantErr: true},
		{expr: ".", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got, err := Eval(tt.expr)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Eval(%q) = %v, want error", tt.expr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Eval(%q) error: %v", tt.expr, err)
			}
			if math.Abs(got-tt.want) > 1e-12 {
				t.Errorf("Eval(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		v    float64
		want string
	}{
		{name: "integer", v: 8, want: "8"},
		{name: "negative integer", v: -12, want: "-12"},
		{name: "zero", v: 0, want: "0"},
		{name: "float noise collapses", v: 0.1 + 0.2, want: "0.3"},
		{name: "third", v: 1.0 / 3.0, want: "0.333333333333"},
		{name: "large integral switches to exponent", v: 1e20, want: "1e+20"},
		{name: "small value", v: math.Pow(2, -20), want: "9.53674316406e-07"},
		{name: "plain float", v: 2.5, want: "2.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Format(tt.v); got != tt.want {
				t.Errorf("Format(%v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}
