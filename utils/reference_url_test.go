package utils

import "testing"

func TestValidateReferenceURL(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"https://www.flipkart.com/p/itm1?pid=X&utm_source=a&gclid=b#rd", "https://www.flipkart.com/p/itm1?pid=X", true},
		{"  www.meesho.com/x/p/1 \n", "https://www.meesho.com/x/p/1", true},
		{"HTTPS://Shop.Example/P", "https://Shop.Example/P", true},
		{"https://user:pw@shop.example/p", "https://shop.example/p", true},
		{"javascript:alert(1)", "", false},
		{"file:///etc/passwd", "", false},
		{"intent://scan/#Intent;scheme=zxing;end", "", false},
		{"", "", false},
		{"https://", "", false},
	}
	for _, c := range cases {
		got, err := ValidateReferenceURL(c.in)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("%q: got (%q, %v), want %q", c.in, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("%q: expected error, got %q", c.in, got)
		}
	}
}

func TestValidateReferenceURL_Length(t *testing.T) {
	long := "https://shop.example/" + string(make([]byte, MaxReferenceURLLen))
	if _, err := ValidateReferenceURL(long); err == nil {
		t.Error("over-long url accepted")
	}
}

func TestHostOfURL(t *testing.T) {
	if h := HostOfURL("https://WWW.Myntra.com/x?y=1"); h != "myntra.com" {
		t.Errorf("got %q", h)
	}
	if h := HostOfURL("not a url"); h != "" {
		t.Errorf("got %q for garbage", h)
	}
}
