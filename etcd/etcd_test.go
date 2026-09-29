package etcd

import "testing"

func TestMapKey(t *testing.T) {
	cases := []struct {
		src  *Source
		key  string
		want string
	}{
		{&Source{key: "/myapp/app", section: "app"}, "/myapp/app/port", "app.port"},
		{&Source{key: "/myapp/app", section: ""}, "/myapp/app/port", "port"},
		{&Source{key: "/myapp/app", section: "app"}, "/myapp/app/db/host", "app.db.host"},
		{&Source{key: "/myapp/app", section: "app"}, "/myapp/app", "app.app"},
	}
	for _, c := range cases {
		if got := c.src.mapKey(c.key); got != c.want {
			t.Errorf("mapKey(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestNewRequiresKey(t *testing.T) {
	if _, err := New(Endpoints("127.0.0.1:1"), Prefix(true)); err == nil {
		t.Fatal("expected error when Key is missing")
	}
}
