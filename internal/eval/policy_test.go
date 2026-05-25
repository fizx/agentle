package eval

import "testing"

func bptr(b bool) *bool { return &b }

func TestAnnotationWrite(t *testing.T) {
	cases := []struct {
		name                 string
		ro, destr, idem      *bool
		wantWrite, wantKnown bool
	}{
		{"readonly => read", bptr(true), nil, nil, false, true},
		{"destructive => write", nil, bptr(true), nil, true, true},
		{"idempotent => read", nil, nil, bptr(true), false, true},
		{"non-idempotent => write", nil, nil, bptr(false), true, true},
		{"no hints => unknown (write)", nil, nil, nil, true, false},
		{"readonly wins over nothing", bptr(true), bptr(false), nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, known := AnnotationWrite(c.ro, c.destr, c.idem)
			if w != c.wantWrite || known != c.wantKnown {
				t.Fatalf("got write=%v known=%v, want %v/%v", w, known, c.wantWrite, c.wantKnown)
			}
		})
	}
}

func TestHostMethodClassifier(t *testing.T) {
	c := HostMethodClassifier{Table: map[string]bool{
		PolicyKey("api.example.com", "GET"):  false, // operator says GET here is a read
		PolicyKey("api.example.com", "POST"): true,
	}}
	get := mustArgs("https://api.example.com/users", "")
	post := mustArgs("https://api.example.com/orders", "{}")
	unknown := mustArgs("https://other.com/thing", "")

	if c.IsWrite("get", get) {
		t.Fatal("table read should not be a write")
	}
	if !c.IsWrite("post", post) {
		t.Fatal("table write should be a write")
	}
	// Unknown host => fail-safe write.
	if !c.IsWrite("get", unknown) {
		t.Fatal("unknown tool must default to write")
	}

	// With MethodFallback, an unmatched GET is treated as a read.
	cf := HostMethodClassifier{MethodFallback: true}
	if cf.IsWrite("get", unknown) {
		t.Fatal("method fallback should treat unmatched GET as read")
	}
	if !cf.IsWrite("post", unknown) {
		t.Fatal("method fallback still writes for POST")
	}
}
