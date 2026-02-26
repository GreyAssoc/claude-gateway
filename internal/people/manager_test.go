package people

import "testing"

// TestNewManagerNilDBPanics verifies that NewManager panics when db is nil,
// consistent with ctxbuilder.NewBuilder's nil-guard behavior.
func TestNewManagerNilDBPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewManager(nil, nil) did not panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is %T, want string", r)
		}
		if msg != "people: nil db" {
			t.Errorf("panic message = %q, want %q", msg, "people: nil db")
		}
	}()

	NewManager(nil, nil)
}
