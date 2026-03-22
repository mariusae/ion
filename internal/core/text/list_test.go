package text

import (
	"reflect"
	"testing"
)

func TestListInsertDelete(t *testing.T) {
	t.Parallel()

	var list List[Posn]
	list.Insert(0, 10)
	list.Insert(1, 30)
	list.Insert(1, 20)
	if got, want := list.Items(), []Posn{10, 20, 30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after insert got %v want %v", got, want)
	}

	list.Delete(1)
	if got, want := list.Items(), []Posn{10, 30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after delete got %v want %v", got, want)
	}
}
