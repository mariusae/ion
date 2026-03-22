package text

// List is the growable ordered container used by the ported core.
//
// The zero value is ready for use.
type List[T any] struct {
	items []T
}

// Len reports the number of stored elements.
func (l *List[T]) Len() int {
	return len(l.items)
}

// Items exposes the current contents.
func (l *List[T]) Items() []T {
	return l.items
}

// Insert inserts value at index i.
func (l *List[T]) Insert(i int, value T) {
	var zero T
	l.items = append(l.items, zero)
	copy(l.items[i+1:], l.items[i:])
	l.items[i] = value
}

// Delete removes the ith element.
func (l *List[T]) Delete(i int) {
	copy(l.items[i:], l.items[i+1:])
	var zero T
	l.items[len(l.items)-1] = zero
	l.items = l.items[:len(l.items)-1]
}
